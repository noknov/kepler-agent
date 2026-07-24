package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore replaces the directory-of-JSON implementation in production.
type PGStore struct {
	pool    *pgxpool.Pool
	ownPool bool
}

// NewPGStoreWithPool creates a PGStore using an externally managed pool.
func NewPGStoreWithPool(ctx context.Context, pool *pgxpool.Pool) (*PGStore, error) {
	s := &PGStore{pool: pool}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PGStore) Close() {
	if s != nil && s.pool != nil && s.ownPool {
		s.pool.Close()
	}
}
func (s *PGStore) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS agent_runs (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL, started_at TIMESTAMPTZ NOT NULL, slack_channel TEXT NOT NULL DEFAULT '', slack_message_ts TEXT NOT NULL DEFAULT '', payload JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_runs_session_started ON agent_runs(session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_runs_slack_message ON agent_runs(slack_channel, slack_message_ts) WHERE slack_message_ts <> '';`)
	return err
}
func (s *PGStore) Save(ctx context.Context, run Run) error {
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	// PostgreSQL JSONB rejects NUL; preserve all other historical run content.
	b = bytes.ReplaceAll(b, []byte(`\u0000`), nil)
	_, err = s.pool.Exec(ctx, `INSERT INTO agent_runs(id,session_id,started_at,slack_channel,slack_message_ts,payload) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET slack_channel=EXCLUDED.slack_channel,slack_message_ts=EXCLUDED.slack_message_ts,payload=EXCLUDED.payload`, run.ID, run.SessionID, run.StartedAt, run.SlackChannel, run.SlackMessageTS, b)
	return err
}
func (s *PGStore) Get(ctx context.Context, id string) (Run, bool, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM agent_runs WHERE id=$1`, id).Scan(&b)
	if err == pgx.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	var r Run
	err = json.Unmarshal(b, &r)
	return r, err == nil, err
}
func (s *PGStore) List(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.list(ctx, `SELECT payload FROM agent_runs ORDER BY started_at DESC LIMIT $1`, limit)
}
func (s *PGStore) ListBySession(ctx context.Context, id string) ([]Run, error) {
	return s.list(ctx, `SELECT payload FROM agent_runs WHERE session_id=$1 ORDER BY started_at DESC`, id)
}
func (s *PGStore) list(ctx context.Context, q string, args ...any) ([]Run, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var r Run
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *PGStore) AddFeedback(ctx context.Context, id string, fb Feedback) error {
	r, ok, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("run not found")
	}
	r.Feedback = append(r.Feedback, fb)
	r.Quality = scoreRun(r)
	return s.Save(ctx, r)
}
func (s *PGStore) AddFeedbackForMessage(ctx context.Context, ch, ts string, fb Feedback) (string, bool, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM agent_runs WHERE slack_channel=$1 AND slack_message_ts=$2 ORDER BY started_at DESC LIMIT 1`, ch, ts).Scan(&b)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return "", false, err
	}
	r.Feedback = append(r.Feedback, fb)
	r.Quality = scoreRun(r)
	return r.ID, true, s.Save(ctx, r)
}
