// Package eventinbox provides a durable Slack Events API inbox.
package eventinbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Record struct {
	ID      string
	Payload json.RawMessage
}
type PGStore struct {
	pool    *pgxpool.Pool
	owner   string
	ownPool bool
}

// NewPGStoreWithPool creates a PGStore using an externally managed pool.
func NewPGStoreWithPool(ctx context.Context, pool *pgxpool.Pool) (*PGStore, error) {
	s := &PGStore{pool: pool, owner: defaultOwner()}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PGStore) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS slack_event_inbox (
	event_id TEXT PRIMARY KEY,
	payload JSONB NOT NULL,
	status TEXT NOT NULL DEFAULT 'queued',
	received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	started_at TIMESTAMPTZ,
	claim_until TIMESTAMPTZ,
	claim_owner TEXT NOT NULL DEFAULT '',
	completed_at TIMESTAMPTZ
);
ALTER TABLE slack_event_inbox ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
ALTER TABLE slack_event_inbox ADD COLUMN IF NOT EXISTS claim_until TIMESTAMPTZ;
ALTER TABLE slack_event_inbox ADD COLUMN IF NOT EXISTS claim_owner TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_slack_event_inbox_pending ON slack_event_inbox(received_at) WHERE status='queued';
CREATE INDEX IF NOT EXISTS idx_slack_event_inbox_expired ON slack_event_inbox(claim_until) WHERE status='processing';
`)
	return err
}

func (s *PGStore) Close() {
	if s != nil && s.pool != nil && s.ownPool {
		s.pool.Close()
	}
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
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='completed',completed_at=NOW(),claim_until=NULL WHERE event_id=$1 AND claim_owner=$2`, id, s.owner)
	return err
}

func (s *PGStore) Requeue(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='queued',claim_until=NULL,claim_owner='' WHERE event_id=$1 AND status='processing' AND claim_owner=$2`, id, s.owner)
	return err
}

// Start atomically claims one queued event. Duplicate webhook deliveries may
// enqueue duplicate in-memory jobs, but only one worker gets true here.
func (s *PGStore) Start(ctx context.Context, id string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = 16 * time.Minute
	}
	tag, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='processing',started_at=COALESCE(started_at,NOW()),claim_until=NOW()+$3::interval,claim_owner=$2 WHERE event_id=$1 AND status='queued'`, id, s.owner, intervalLiteral(lease))
	return tag.RowsAffected() == 1, err
}

// RecoverExpired releases events whose worker stopped renewing ownership before
// finishing. Unlike a blanket processing reset, this is safe with many replicas:
// a healthy pod keeps its unexpired claim, while work abandoned by a dead pod is
// retried after the lease expires.
func (s *PGStore) RecoverExpired(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='queued',claim_until=NULL,claim_owner='' WHERE status='processing' AND (claim_until IS NULL OR claim_until < NOW())`)
	return err
}
func (s *PGStore) Pending(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 512
	}
	rows, err := s.pool.Query(ctx, `SELECT event_id,payload FROM slack_event_inbox WHERE status='queued' ORDER BY received_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Payload); err != nil {
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
