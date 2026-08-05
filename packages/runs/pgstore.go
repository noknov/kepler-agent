package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore replaces the directory-of-JSON implementation in production.
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }
func (s *PGStore) Save(ctx context.Context, run Run) error {
	// Steps and feedback are append-only child records. Keeping them out of the
	// aggregate payload prevents quadratic write amplification and stale writers
	// from overwriting feedback received while a run is still active.
	aggregate := run
	aggregate.Steps = nil
	aggregate.Feedback = nil
	b, err := json.Marshal(aggregate)
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
	if err = json.Unmarshal(b, &r); err != nil {
		return Run{}, false, err
	}
	if err = s.loadChildren(ctx, &r); err != nil {
		return Run{}, false, err
	}
	return r, true, nil
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

func (s *PGStore) UserAuditSummaries(ctx context.Context, start, end time.Time) ([]UserAuditSummary, error) {
	rows, err := s.pool.Query(ctx, `
SELECT
 payload->>'user_id' AS user_id,
 COUNT(*)::int AS requests,
 COUNT(DISTINCT NULLIF(payload->>'session_id', ''))::int AS conversations,
 COUNT(*) FILTER (WHERE payload->>'status' = 'completed')::int AS completed,
 COUNT(*) FILTER (WHERE payload->>'status' = 'error')::int AS failed,
 COALESCE(SUM((payload->'usage'->>'prompt_tokens')::bigint), 0) AS prompt_tokens,
 COALESCE(SUM((payload->'usage'->>'completion_tokens')::bigint), 0) AS completion_tokens,
 COALESCE(SUM(COALESCE(NULLIF(payload->'usage'->>'total_tokens', '')::bigint,
   COALESCE((payload->'usage'->>'prompt_tokens')::bigint, 0) +
   COALESCE((payload->'usage'->>'completion_tokens')::bigint, 0) +
   COALESCE((payload->'usage'->>'cache_read_input_tokens')::bigint, 0) +
   COALESCE((payload->'usage'->>'cache_creation_input_tokens')::bigint, 0)
 )), 0) AS total_tokens,
 COALESCE(SUM((payload->>'estimated_cost_usd')::double precision), 0) AS estimated_cost_usd,
 MIN(started_at) AS first_started_at,
 MAX(started_at) AS last_started_at
FROM agent_runs
WHERE started_at >= $1 AND started_at < $2 AND payload->>'user_id' <> ''
GROUP BY payload->>'user_id'
ORDER BY last_started_at DESC`, start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserAuditSummary
	for rows.Next() {
		var summary UserAuditSummary
		if err := rows.Scan(&summary.UserID, &summary.Requests, &summary.Conversations, &summary.Completed, &summary.Failed, &summary.PromptTokens, &summary.CompletionTokens, &summary.TotalTokens, &summary.EstimatedCostUSD, &summary.FirstStartedAt, &summary.LastStartedAt); err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, rows.Err()
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
		if err := s.loadChildren(ctx, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *PGStore) AddFeedback(ctx context.Context, id string, fb Feedback) error {
	return s.insertFeedback(ctx, id, fb)
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
	return r.ID, true, s.insertFeedback(ctx, r.ID, fb)
}

func (s *PGStore) AppendStep(ctx context.Context, runID string, step Step) error {
	b, err := json.Marshal(step)
	if err != nil {
		return err
	}
	b = bytes.ReplaceAll(b, []byte(`\u0000`), nil)
	_, err = s.pool.Exec(ctx, `INSERT INTO agent_run_steps(run_id,step_id,started_at,payload) VALUES($1,$2,$3,$4) ON CONFLICT(run_id,step_id) DO NOTHING`, runID, step.ID, step.StartedAt, b)
	return err
}

func (s *PGStore) loadChildren(ctx context.Context, run *Run) error {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM agent_run_steps WHERE run_id=$1 ORDER BY seq`, run.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}
		var step Step
		if err := json.Unmarshal(b, &step); err != nil {
			return err
		}
		run.Steps = append(run.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	feedbackRows, err := s.pool.Query(ctx, `SELECT source,value,user_id,channel,message_ts,created_at FROM agent_run_feedback WHERE run_id=$1 ORDER BY created_at`, run.ID)
	if err != nil {
		return err
	}
	defer feedbackRows.Close()
	for feedbackRows.Next() {
		var fb Feedback
		if err := feedbackRows.Scan(&fb.Source, &fb.Value, &fb.UserID, &fb.Channel, &fb.MessageTS, &fb.CreatedAt); err != nil {
			return err
		}
		run.Feedback = append(run.Feedback, fb)
	}
	if err := feedbackRows.Err(); err != nil {
		return err
	}
	if len(run.Feedback) > 0 {
		run.Quality = scoreRun(*run)
	}
	return nil
}

func (s *PGStore) insertFeedback(ctx context.Context, runID string, fb Feedback) error {
	if fb.CreatedAt.IsZero() {
		fb.CreatedAt = time.Now().UTC()
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO agent_run_feedback(run_id,source,value,user_id,channel,message_ts,created_at)
SELECT id,$2,$3,$4,$5,$6,$7 FROM agent_runs WHERE id=$1
ON CONFLICT DO NOTHING`, runID, fb.Source, fb.Value, fb.UserID, fb.Channel, fb.MessageTS, fb.CreatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1)`, runID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("run not found")
		}
	}
	return nil
}

func (s *PGStore) SaveToolSpill(ctx context.Context, runID, toolName, toolCallID, content string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("run store is unavailable")
	}
	content = strings.ReplaceAll(content, "\x00", "")
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_tool_spills(run_id,tool_name,tool_call_id,content,created_at,updated_at)
VALUES($1,$2,$3,$4,NOW(),NOW())
ON CONFLICT(run_id, tool_name, tool_call_id) DO UPDATE SET content=EXCLUDED.content, updated_at=NOW()`,
		runID, toolName, toolCallID, content)
	return err
}

func (s *PGStore) ReadToolSpill(ctx context.Context, runID, toolName, toolCallID string) (string, error) {
	if s == nil || s.pool == nil {
		return "", fmt.Errorf("run store is unavailable")
	}
	var content string
	err := s.pool.QueryRow(ctx, `SELECT content FROM agent_tool_spills WHERE run_id=$1 AND tool_name=$2 AND tool_call_id=$3`,
		runID, toolName, toolCallID).Scan(&content)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("persisted output not found")
	}
	if err != nil {
		return "", err
	}
	return content, nil
}
