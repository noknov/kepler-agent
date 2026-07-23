// Package eventinbox provides a durable Slack Events API inbox.
package eventinbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Record struct {
	ID      string
	Payload json.RawMessage
}
type PGStore struct{ pool *pgxpool.Pool }

func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse inbox postgres dsn: %w", err)
	}
	cfg.MaxConns, cfg.MinConns = 6, 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect inbox postgres: %w", err)
	}
	s := &PGStore{pool: pool}
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS slack_event_inbox (event_id TEXT PRIMARY KEY, payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'queued', received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), completed_at TIMESTAMPTZ); CREATE INDEX IF NOT EXISTS idx_slack_event_inbox_pending ON slack_event_inbox(received_at) WHERE status='queued';`)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}
func (s *PGStore) Close() {
	if s != nil && s.pool != nil {
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
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='completed',completed_at=NOW() WHERE event_id=$1`, id)
	return err
}

func (s *PGStore) Requeue(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='queued' WHERE event_id=$1 AND status='processing'`, id)
	return err
}

// Start atomically claims one queued event. Duplicate webhook deliveries may
// enqueue duplicate in-memory jobs, but only one worker gets true here.
func (s *PGStore) Start(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='processing' WHERE event_id=$1 AND status='queued'`, id)
	return tag.RowsAffected() == 1, err
}

// Recover resets work interrupted by a process crash before pending events are
// replayed. Processing is deliberately not terminal until Complete succeeds.
func (s *PGStore) Recover(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE slack_event_inbox SET status='queued' WHERE status='processing'`)
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
