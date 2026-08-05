package reminder

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is a PostgreSQL-backed reminder store. Due atomically leases rows,
// making delivery safe when several slack-copilot-agent instances are running.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore uses a shared pool and assumes schema/postgres.sql is installed.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }
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
