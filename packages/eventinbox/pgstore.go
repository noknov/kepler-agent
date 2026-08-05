// Package eventinbox provides a durable Slack Events API inbox.
package eventinbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Record struct {
	ID       string
	Payload  json.RawMessage
	Attempts int
}

var ErrLeaseLost = errors.New("event inbox lease ownership lost")

type Store interface {
	Start(context.Context, string, time.Duration) (bool, error)
	Renew(context.Context, string, time.Duration) error
	Complete(context.Context, string) error
	Fail(context.Context, string, error, time.Duration, int) (bool, error)
	DeadLetter(context.Context, string, error) error
	RecoverExpired(context.Context, int) error
	Pending(context.Context, int) ([]Record, error)
}
type PGStore struct {
	pool  *pgxpool.Pool
	owner string
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, owner: defaultOwner()}
}
func (s *PGStore) Claim(ctx context.Context, id string, payload any) (bool, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO slack_event_inbox(event_id,payload) VALUES($1,$2) ON CONFLICT(event_id) DO NOTHING`, id, b)
	return tag.RowsAffected() == 1, err
}
func (s *PGStore) Complete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='completed',completed_at=NOW(),claim_until=NULL,claim_owner='' WHERE event_id=$1 AND status='processing' AND claim_owner=$2`, id, s.owner)
	return ownershipResult(tag.RowsAffected(), err)
}

func (s *PGStore) Renew(ctx context.Context, id string, lease time.Duration) error {
	if lease <= 0 {
		return fmt.Errorf("renew event lease: lease must be positive")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET claim_until=NOW()+$3::interval WHERE event_id=$1 AND status='processing' AND claim_owner=$2`, id, s.owner, intervalLiteral(lease))
	return ownershipResult(tag.RowsAffected(), err)
}

// Fail releases a claimed event with bounded exponential backoff. It returns
// true when the event exhausted its attempt budget and was dead-lettered.
func (s *PGStore) Fail(ctx context.Context, id string, cause error, retryDelay time.Duration, maxAttempts int) (bool, error) {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if retryDelay < 0 {
		retryDelay = 0
	}
	message := sanitizeError(cause)
	var status string
	err := s.pool.QueryRow(ctx, `UPDATE slack_event_inbox SET
status=CASE WHEN attempt_count >= $3 THEN 'dead_letter' ELSE 'queued' END,
available_at=CASE WHEN attempt_count >= $3 THEN available_at ELSE NOW()+$4::interval END,
dead_lettered_at=CASE WHEN attempt_count >= $3 THEN NOW() ELSE NULL END,
last_error=$5,claim_until=NULL,claim_owner=''
WHERE event_id=$1 AND status='processing' AND claim_owner=$2
RETURNING status`, id, s.owner, maxAttempts, intervalLiteral(retryDelay), message).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrLeaseLost
	}
	return status == "dead_letter", err
}

func (s *PGStore) DeadLetter(ctx context.Context, id string, cause error) error {
	tag, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='dead_letter',dead_lettered_at=NOW(),last_error=$2,claim_until=NULL,claim_owner='' WHERE event_id=$1 AND status IN ('queued','processing')`, id, sanitizeError(cause))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("dead-letter event %s: event is not active", id)
	}
	return nil
}

// Start atomically claims one queued event. Duplicate webhook deliveries may
// enqueue duplicate in-memory jobs, but only one worker gets true here.
func (s *PGStore) Start(ctx context.Context, id string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = 16 * time.Minute
	}
	tag, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='processing',started_at=COALESCE(started_at,NOW()),claim_until=NOW()+$3::interval,claim_owner=$2,attempt_count=attempt_count+1 WHERE event_id=$1 AND status='queued' AND available_at <= NOW()`, id, s.owner, intervalLiteral(lease))
	return tag.RowsAffected() == 1, err
}

// RecoverExpired releases events whose worker stopped renewing ownership before
// finishing. Unlike a blanket processing reset, this is safe with many replicas:
// a healthy pod keeps its unexpired claim, while work abandoned by a dead pod is
// retried after the lease expires.
func (s *PGStore) RecoverExpired(ctx context.Context, maxAttempts int) error {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET
status=CASE WHEN attempt_count >= $1 THEN 'dead_letter' ELSE 'queued' END,
available_at=NOW(),dead_lettered_at=CASE WHEN attempt_count >= $1 THEN NOW() ELSE NULL END,
last_error='worker lease expired',claim_until=NULL,claim_owner=''
WHERE status='processing' AND (claim_until IS NULL OR claim_until < NOW())`, maxAttempts)
	return err
}
func (s *PGStore) Pending(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 512
	}
	rows, err := s.pool.Query(ctx, `SELECT event_id,payload,attempt_count FROM slack_event_inbox WHERE status='queued' AND available_at <= NOW() ORDER BY available_at,received_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Payload, &r.Attempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *PGStore) Prune(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM slack_event_inbox WHERE status='completed' AND completed_at < NOW()-$1::interval`, fmt.Sprintf("%f seconds", olderThan.Seconds()))
	return err
}

func defaultOwner() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}

func intervalLiteral(d time.Duration) string {
	return fmt.Sprintf("%f seconds", d.Seconds())
}

func ownershipResult(rows int64, err error) error {
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), "\x00", "")
	const max = 2048
	if len(message) > max {
		message = message[:max]
	}
	return message
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
