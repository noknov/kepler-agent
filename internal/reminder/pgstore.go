package reminder

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is a PostgreSQL-backed reminder store. Due atomically leases rows,
// making delivery safe when several oncall-agent instances are running.
type PGStore struct{ pool *pgxpool.Pool }

func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	if dsn == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse reminder postgres dsn: %w", err)
	}
	cfg.MaxConns, cfg.MinConns = 5, 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect reminder postgres: %w", err)
	}
	store := &PGStore{pool: pool}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}
func (s *PGStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}
func (s *PGStore) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS reminders (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL, channel TEXT NOT NULL, thread_ts TEXT NOT NULL DEFAULT '',
 message TEXT NOT NULL, run_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 sent_at TIMESTAMPTZ, claim_until TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_reminders_due ON reminders (run_at) WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reminders_user_pending ON reminders (user_id, run_at) WHERE sent_at IS NULL;`)
	if err != nil {
		return fmt.Errorf("migrate reminders: %w", err)
	}
	return nil
}
func (s *PGStore) Create(ctx context.Context, r Reminder) (Reminder, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO reminders (id,user_id,channel,thread_ts,message,run_at) VALUES ($1,$2,$3,$4,$5,$6) RETURNING created_at`, r.ID, r.UserID, r.Channel, r.ThreadTS, r.Message, r.RunAt).Scan(&r.CreatedAt)
	if err != nil {
		return Reminder{}, fmt.Errorf("create reminder: %w", err)
	}
	return r, nil
}
func (s *PGStore) List(ctx context.Context, userID string) ([]Reminder, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,user_id,channel,thread_ts,message,run_at,created_at,sent_at FROM reminders WHERE user_id=$1 AND sent_at IS NULL ORDER BY run_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminders(rows)
}
func (s *PGStore) Due(ctx context.Context, now time.Time) ([]Reminder, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT id FROM reminders WHERE sent_at IS NULL AND run_at <= $1 AND (claim_until IS NULL OR claim_until < $1)
 ORDER BY run_at FOR UPDATE SKIP LOCKED LIMIT 100
) UPDATE reminders r SET claim_until=$1 + INTERVAL '1 minute' FROM candidates c WHERE r.id=c.id
RETURNING r.id,r.user_id,r.channel,r.thread_ts,r.message,r.run_at,r.created_at,r.sent_at`, now.UTC())
	if err != nil {
		return nil, err
	}
	out, err := scanReminders(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *PGStore) MarkSent(ctx context.Context, id string, sentAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE reminders SET sent_at=$2,claim_until=NULL WHERE id=$1 AND sent_at IS NULL`, id, sentAt.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("reminder not found")
	}
	return nil
}
func (s *PGStore) Cancel(ctx context.Context, id, userID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE reminders SET sent_at=NOW(),claim_until=NULL WHERE id=$1 AND user_id=$2 AND sent_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("reminder not found")
	}
	return nil
}
func scanReminders(rows pgx.Rows) ([]Reminder, error) {
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var sentAt *time.Time
		if err := rows.Scan(&r.ID, &r.UserID, &r.Channel, &r.ThreadTS, &r.Message, &r.RunAt, &r.CreatedAt, &sentAt); err != nil {
			return nil, err
		}
		if sentAt != nil {
			r.SentAt = *sentAt
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
