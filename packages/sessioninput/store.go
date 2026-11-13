// Package sessioninput stores durable commands directed at an agent session.
package sessioninput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Kind string

const (
	KindSteering Kind = "steering"
	KindQueue    Kind = "queue"
	KindStop     Kind = "stop"
)

var ErrClaimLost = errors.New("session input claim lost")

type Item struct {
	ID        string
	SessionID string
	Kind      Kind
	Payload   json.RawMessage
	Sequence  int64
	Attempts  int
}

type Store interface {
	Enqueue(context.Context, Item) error
	Claim(context.Context, string, Kind, string, time.Duration, int) ([]Item, error)
	Ack(context.Context, string, string) error
	Renew(context.Context, string, string, time.Duration) error
	Fail(context.Context, string, string, error, time.Duration, int) (bool, error)
	Release(context.Context, string, string) error
	Requeue(context.Context, string, string) error
	PendingSessions(context.Context, Kind, int) ([]string, error)
	PromoteSteering(context.Context, string) (int64, error)
	PromoteExpiredSteering(context.Context, time.Duration) (int64, error)
}

type PGStore struct{ pool *pgxpool.Pool }

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Enqueue(ctx context.Context, item Item) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("session input store is unavailable")
	}
	if item.ID == "" || item.SessionID == "" || (item.Kind != KindSteering && item.Kind != KindQueue && item.Kind != KindStop) || len(item.Payload) == 0 {
		return fmt.Errorf("complete session input is required")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_session_inputs(id,session_id,kind,payload,created_at)
VALUES($1,$2,$3,$4,NOW()) ON CONFLICT(id) DO NOTHING`, item.ID, item.SessionID, item.Kind, item.Payload)
	return err
}

func (s *PGStore) Claim(ctx context.Context, sessionID string, kind Kind, owner string, lease time.Duration, limit int) ([]Item, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("session input store is unavailable")
	}
	if sessionID == "" || owner == "" || lease <= 0 {
		return nil, fmt.Errorf("session, owner, and positive lease are required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	leaseSeconds := int64((lease + time.Second - 1) / time.Second)
	rows, err := s.pool.Query(ctx, `WITH picked AS (
  SELECT id FROM agent_session_inputs
  WHERE session_id=$1 AND kind=$2 AND acknowledged_at IS NULL AND dead_lettered_at IS NULL AND available_at<=NOW()
    AND (claim_until IS NULL OR claim_until<NOW() OR claim_owner=$3)
  ORDER BY sequence FOR UPDATE SKIP LOCKED LIMIT $4
), claimed AS (
  UPDATE agent_session_inputs input
  SET claim_owner=$3, claim_until=NOW()+make_interval(secs => $5), attempts=attempts+1
  FROM picked WHERE input.id=picked.id
  RETURNING input.id,input.session_id,input.kind,input.payload,input.sequence,input.attempts
)
SELECT id,session_id,kind,payload,sequence,attempts FROM claimed ORDER BY sequence`, sessionID, kind, owner, limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Kind, &item.Payload, &item.Sequence, &item.Attempts); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PGStore) Ack(ctx context.Context, id, owner string) error {
	if s == nil || s.pool == nil || id == "" || owner == "" {
		return fmt.Errorf("session input store, id, and owner are required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_session_inputs SET acknowledged_at=NOW(),claim_until=NULL
WHERE id=$1 AND claim_owner=$2 AND acknowledged_at IS NULL`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

func (s *PGStore) Release(ctx context.Context, id, owner string) error {
	if s == nil || s.pool == nil || id == "" || owner == "" {
		return fmt.Errorf("session input store, id, and owner are required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_session_inputs SET claim_owner='',claim_until=NULL
WHERE id=$1 AND claim_owner=$2 AND acknowledged_at IS NULL`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

func (s *PGStore) PendingSessions(ctx context.Context, kind Kind, limit int) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("session input store is unavailable")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id FROM agent_session_inputs
WHERE kind=$1 AND acknowledged_at IS NULL AND dead_lettered_at IS NULL AND available_at<=NOW() AND (claim_until IS NULL OR claim_until<NOW())
GROUP BY session_id ORDER BY MIN(sequence) LIMIT $2`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var session string
		if err := rows.Scan(&session); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *PGStore) PromoteExpiredSteering(ctx context.Context, age time.Duration) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("session input store is unavailable")
	}
	if age <= 0 {
		age = time.Minute
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_session_inputs steering SET kind='queue',claim_owner='',claim_until=NULL
WHERE kind='steering' AND acknowledged_at IS NULL
  AND (claim_until IS NULL OR claim_until<NOW()) AND created_at<NOW()-make_interval(secs => $1)
 AND NOT EXISTS(SELECT 1 FROM agent_session_inputs parent
 WHERE parent.session_id=steering.session_id AND parent.kind='queue'
 AND parent.acknowledged_at IS NULL AND parent.dead_lettered_at IS NULL
 AND parent.claim_until>NOW() AND parent.claim_owner<>'')`, int64(age/time.Second))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PGStore) PromoteSteering(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.pool == nil || sessionID == "" {
		return 0, fmt.Errorf("session input store and session are required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_session_inputs SET kind='queue',claim_owner='',claim_until=NULL
WHERE session_id=$1 AND kind='steering' AND acknowledged_at IS NULL
  AND (claim_until IS NULL OR claim_until<NOW())`, sessionID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var _ Store = (*PGStore)(nil)

// Requeue moves a claimed steering input to its actor's next turn atomically.
// The consumer owns the security decision; a routing hint cannot authorize it.
func (s *PGStore) Requeue(ctx context.Context, id, owner string) error {
	if s == nil || s.pool == nil || id == "" || owner == "" {
		return fmt.Errorf("session input store, id, and owner are required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_session_inputs SET kind='queue',claim_owner='',claim_until=NULL
 WHERE id=$1 AND claim_owner=$2 AND kind='steering' AND acknowledged_at IS NULL`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

func (s *PGStore) Renew(ctx context.Context, id, owner string, lease time.Duration) error {
	if s == nil || s.pool == nil || id == "" || owner == "" || lease <= 0 {
		return fmt.Errorf("input claim renewal requires store, identity and lease")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_session_inputs SET claim_until=NOW()+make_interval(secs => $3)
 WHERE id=$1 AND claim_owner=$2 AND acknowledged_at IS NULL`, id, owner, int64((lease+time.Second-1)/time.Second))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

// Fail releases a failed execution with a persistent retry deadline. Exhausted
// inputs remain visibly dead-lettered; they are never acknowledged as success.
func (s *PGStore) Fail(ctx context.Context, id, owner string, cause error, delay time.Duration, maxAttempts int) (bool, error) {
	if s == nil || s.pool == nil || id == "" || owner == "" || delay < 0 || maxAttempts < 1 {
		return false, fmt.Errorf("invalid session input failure")
	}
	var dead bool
	err := s.pool.QueryRow(ctx, `UPDATE agent_session_inputs SET
 available_at=NOW()+make_interval(secs => $3),last_error=$4,
 dead_lettered_at=CASE WHEN attempts>=$5 THEN NOW() ELSE NULL END,
 claim_owner='',claim_until=NULL WHERE id=$1 AND claim_owner=$2 AND acknowledged_at IS NULL
 RETURNING dead_lettered_at IS NOT NULL`, id, owner, delay.Seconds(), fmt.Sprintf("%T", cause), maxAttempts).Scan(&dead)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrClaimLost
	}
	return dead, err
}
