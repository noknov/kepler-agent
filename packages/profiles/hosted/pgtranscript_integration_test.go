package hosted

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/runs"
)

func TestIntegrationPGTranscriptAppendAndReplay(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sessionID := "test-transcript-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
	}()
	store := PGTranscript{Pool: pool}
	first, err := store.Append(context.Background(), transcript.Event{ID: sessionID + "-1", SessionID: sessionID, Type: transcript.SessionStarted, Timestamp: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(context.Background(), transcript.Event{ID: sessionID + "-2", SessionID: sessionID, TurnID: "turn", Type: transcript.TurnStarted, Timestamp: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(context.Background(), sessionID, first.Sequence)
	if err != nil || len(events) != 1 || events[0].Sequence != second.Sequence {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestIntegrationRunSinkRecoversInterruptedProjection(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	sessionID, turnID := "test-recover-session-"+suffix, "test-recover-turn-"+suffix
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_run_feedback WHERE run_id=$1`, turnID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_tool_spills WHERE run_id=$1`, turnID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_run_steps WHERE run_id=$1`, turnID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_runs WHERE id=$1`, turnID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
	}()

	start := time.Now().UTC().Add(-5 * time.Second)
	startedMetadata, _ := json.Marshal(map[string]any{"user_id": "integration", "scope": map[string]string{"channel": "C1"}, "model": "recovery-model"})
	usageMetadata, _ := json.Marshal(map[string]any{"usage": model.Usage{InputTokens: 8, OutputTokens: 3}, "finish_reason": model.FinishStop})
	answer := model.TextMessage(model.RoleAssistant, "recovered answer")
	events := []transcript.Event{
		{ID: turnID + "-start", SessionID: sessionID, TurnID: turnID, Type: transcript.TurnStarted, Timestamp: start, Metadata: startedMetadata},
		{ID: turnID + "-request", SessionID: sessionID, TurnID: turnID, Type: transcript.ModelRequested, Timestamp: start.Add(time.Second)},
		{ID: turnID + "-model", SessionID: sessionID, TurnID: turnID, Type: transcript.ModelCompleted, Timestamp: start.Add(2 * time.Second), Metadata: usageMetadata},
		{ID: turnID + "-answer", SessionID: sessionID, TurnID: turnID, Type: transcript.AssistantMessage, Timestamp: start.Add(2 * time.Second), Message: &answer},
		{ID: turnID + "-done", SessionID: sessionID, TurnID: turnID, Type: transcript.TurnCompleted, Timestamp: start.Add(3 * time.Second), Status: "completed"},
	}
	transcriptStore := PGTranscript{Pool: pool}
	for _, event := range events {
		if _, err := transcriptStore.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	runStore := runs.NewPGStore(pool)
	// Simulate a worker dying after creating the run but before projecting the
	// remaining durable transcript events.
	(&RunSink{Store: runStore, Provider: "test", Model: "fallback"}).Publish(ctx, events[0])
	recovery := &RunSink{Store: runStore, Provider: "test", Model: "fallback"}
	if err := recovery.Recover(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Recover(ctx, pool); err != nil {
		t.Fatal(err)
	}
	run, ok, err := runStore.Get(ctx, turnID)
	if err != nil || !ok {
		t.Fatalf("run missing: ok=%v err=%v", ok, err)
	}
	if run.Status != "completed" || len(run.Steps) != 1 || run.Usage.TotalTokens != 11 || run.FinalHash == "" {
		t.Fatalf("recovered run=%+v", run)
	}
}
