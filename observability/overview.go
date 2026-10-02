package observabilitysvc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/runs"
)

// Overview contains diagnostic metadata, never conversation or tool content.
// Window statistics are exact for runs started in [start,end); queue and running
// records are current database state across all history, not window failures.
type Overview struct {
	Start        time.Time        `json:"start"`
	End          time.Time        `json:"end"`
	Runs         int64            `json:"runs"`
	Statuses     map[string]int64 `json:"statuses"`
	Terminations map[string]int64 `json:"terminations"`
	FailureRate  *float64         `json:"failure_rate"`
	P50MS        float64          `json:"p50_ms"`
	P95MS        float64          `json:"p95_ms"`
	RecentIssues []runs.Run       `json:"recent_issues"`
	Running      []runs.Run       `json:"running"`
	Inbox        QueueStats       `json:"inbox"`
	Inputs       map[string]int64 `json:"inputs"`
	DeadLetters  []InboxIssue     `json:"dead_letters"`
}

type QueueStats struct {
	Statuses       map[string]int64 `json:"statuses"`
	ExpiredClaims  int64            `json:"expired_claims"`
	OldestQueuedMS float64          `json:"oldest_queued_ms"`
}

type InboxIssue struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	Attempts int       `json:"attempts"`
	Error    string    `json:"error"`
}

func readOverview(ctx context.Context, pool *pgxpool.Pool, start, end time.Time) (Overview, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Overview{}, err
	}
	defer tx.Rollback(ctx)
	result, err := queryOverview(ctx, tx, start, end)
	if err != nil {
		return Overview{}, err
	}
	return result, tx.Commit(ctx)
}

// A transaction interface lets tests use an isolated rollback fixture.
func queryOverview(ctx context.Context, tx pgx.Tx, start, end time.Time) (Overview, error) {
	result := Overview{Start: start, End: end, Statuses: map[string]int64{}, Terminations: map[string]int64{}, Inputs: map[string]int64{}}
	err := tx.QueryRow(ctx, `SELECT count(*),
 COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY (payload->>'duration_ms')::double precision) FILTER (WHERE payload->>'status' <> 'running'),0),
 COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY (payload->>'duration_ms')::double precision) FILTER (WHERE payload->>'status' <> 'running'),0)
 FROM agent_runs WHERE started_at >= $1 AND started_at < $2`, start, end).Scan(&result.Runs, &result.P50MS, &result.P95MS)
	if err != nil {
		return result, fmt.Errorf("run totals: %w", err)
	}
	if err = readCounts(ctx, tx, `SELECT COALESCE(payload->>'status','unknown'),count(*) FROM agent_runs WHERE started_at >= $1 AND started_at < $2 GROUP BY 1`, result.Statuses, start, end); err != nil {
		return result, err
	}
	if err = readCounts(ctx, tx, `SELECT COALESCE(NULLIF(payload->>'termination',''),'unknown'),count(*) FROM agent_runs WHERE started_at >= $1 AND started_at < $2 GROUP BY 1`, result.Terminations, start, end); err != nil {
		return result, err
	}
	// User cancellation, pending input and incomplete work are separate outcomes.
	if terminal := result.Statuses["completed"] + result.Statuses["error"]; terminal > 0 {
		rate := float64(result.Statuses["error"]) / float64(terminal)
		result.FailureRate = &rate
	}
	// Select only metadata. Existing List loads every child step and feedback,
	// which turns a summary page into an expensive N+1 history query.
	const metadata = `jsonb_build_object('id',id,'session_id',session_id,'started_at',started_at,'status',payload->>'status',
 'termination',payload->>'termination','provider',payload->>'provider','model',payload->>'model','trace_id',payload->>'trace_id',
 'root_span_id',payload->>'root_span_id','duration_ms',COALESCE((payload->>'duration_ms')::bigint,0),'error',left(COALESCE(payload->>'error',''),500))`
	result.RecentIssues, err = readRunMetadata(ctx, tx, `SELECT `+metadata+` FROM agent_runs WHERE started_at >= $1 AND started_at < $2
 AND payload->>'status' IN ('error','incomplete','interrupted') ORDER BY started_at DESC LIMIT 20`, start, end)
	if err != nil {
		return result, err
	}
	result.Running, err = readRunMetadata(ctx, tx, `SELECT `+metadata+` FROM agent_runs WHERE payload->>'status'='running' ORDER BY started_at LIMIT 20`)
	if err != nil {
		return result, err
	}
	result.Inbox.Statuses = map[string]int64{}
	if err = readCounts(ctx, tx, `SELECT status,count(*) FROM slack_event_inbox GROUP BY status`, result.Inbox.Statuses); err != nil {
		return result, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='processing' AND claim_until <= $1),
 COALESCE(EXTRACT(EPOCH FROM ($1::timestamptz-min(received_at) FILTER (WHERE status='queued')))*1000,0)
 FROM slack_event_inbox`, end).Scan(&result.Inbox.ExpiredClaims, &result.Inbox.OldestQueuedMS); err != nil {
		return result, err
	}
	if err = readCounts(ctx, tx, `SELECT kind||':'||CASE WHEN claim_until IS NULL THEN 'queued' WHEN claim_until <= $1 THEN 'expired' ELSE 'claimed' END,count(*)
 FROM agent_session_inputs WHERE acknowledged_at IS NULL GROUP BY 1`, result.Inputs, end); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT event_id,COALESCE(dead_lettered_at,received_at),attempt_count,left(last_error,500)
 FROM slack_event_inbox WHERE status='dead_letter' ORDER BY COALESCE(dead_lettered_at,received_at) DESC LIMIT 10`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var issue InboxIssue
		if err = rows.Scan(&issue.ID, &issue.At, &issue.Attempts, &issue.Error); err != nil {
			rows.Close()
			return result, err
		}
		result.DeadLetters = append(result.DeadLetters, issue)
	}
	err = rows.Err()
	rows.Close()
	return result, err
}

func readCounts(ctx context.Context, tx pgx.Tx, query string, out map[string]int64, args ...any) error {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		if err = rows.Scan(&key, &count); err != nil {
			return err
		}
		out[key] = count
	}
	return rows.Err()
}

func readRunMetadata(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]runs.Run, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runs.Run
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var run runs.Run
		if err = json.Unmarshal(data, &run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
