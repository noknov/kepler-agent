package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/runs"
)

func TestIntegrationTerminalErrorReplayAfterJSONBRoundTrip(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, original := range []error{context.Canceled, context.DeadlineExceeded, &model.Error{Kind: model.ErrorContentPolicy, Message: "private refusal"}} {
		t.Run(original.Error(), func(t *testing.T) {
			sessionID := fmt.Sprintf("terminal-classification-%d", time.Now().UnixNano())
			defer func() {
				pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
			}()
			store := PGTranscript{Pool: pool}
			kind, reason := transcript.TurnFailed, agentruntime.TerminationModelError
			if errors.Is(original, context.Canceled) {
				kind, reason = transcript.TurnCanceled, agentruntime.TerminationCanceled
			}
			_, err := store.Append(t.Context(), transcript.Event{ID: sessionID, SessionID: sessionID, TurnID: "turn", Type: kind, Status: string(reason), Error: original.Error(), Failure: transcript.FailureFromError(original), Timestamp: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			client := &jsonbReplayModel{}
			catalog, _ := tool.NewCatalog()
			runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			result, replayErr := runner.RunTurn(t.Context(), agentruntime.TurnRequest{SessionID: sessionID, TurnID: "turn"})
			if replayErr == nil || replayErr.Error() != original.Error() || model.ErrorKindOf(replayErr) != model.ErrorKindOf(original) || errors.Is(replayErr, context.Canceled) != errors.Is(original, context.Canceled) || errors.Is(replayErr, context.DeadlineExceeded) != errors.Is(original, context.DeadlineExceeded) || result.Termination != reason || client.responded {
				t.Fatalf("result=%+v replay=%T %v original=%T %v requested=%v", result, replayErr, replayErr, original, original, client.responded)
			}
		})
	}
}

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

func TestIntegrationPGTranscriptCheckpointWindow(t *testing.T) {
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
	sessionID := "checkpoint-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID) }()
	store := PGTranscript{Pool: pool}
	input := model.TextMessage(model.RoleUser, "original task")
	summary := model.TextMessage(model.RoleUser, "checkpoint")
	for i, event := range []transcript.Event{
		{TurnID: "old", Type: transcript.UserInput, Message: &input},
		{TurnID: "old", Type: transcript.ModelCompleted},
		{TurnID: "old", Type: transcript.CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":2}`)},
		{TurnID: "other", Type: transcript.CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":999999999999999999999999}`)},
	} {
		event.SessionID = sessionID
		event.ID = fmt.Sprintf("%s-%d", sessionID, i)
		event.Timestamp = time.Now()
		if _, err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.LoadTurn(ctx, sessionID, "new")
	if err != nil || len(events) != 3 || events[0].Sequence != 1 || events[1].Sequence != 3 {
		t.Fatalf("window=%v err=%v", events, err)
	}
	replay, err := store.LoadTurn(ctx, sessionID, "old")
	if err != nil || len(replay) != 4 {
		t.Fatalf("replay len=%d err=%v", len(replay), err)
	}
	full, err := store.Load(ctx, sessionID, 0)
	if err != nil || len(full) != 4 {
		t.Fatalf("audit len=%d err=%v", len(full), err)
	}
}

func TestIntegrationDeferredApprovalAfterJSONBRoundTrip(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sessionID := fmt.Sprintf("jsonb-approval-%d", time.Now().UnixNano())
	defer func() {
		pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
	}()
	calls := 0
	item := jsonbApprovalTool{calls: &calls}
	catalog, _ := tool.NewCatalog(item)
	if err := catalog.Activate(sessionID, "write"); err != nil {
		t.Fatal(err)
	}
	store := PGTranscript{Pool: pool}
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: jsonbApprovalModel{}, Tools: catalog, Transcript: store, Policy: Policy{Allowed: map[string]bool{"write": true}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(ctx, agentruntime.TurnRequest{SessionID: sessionID, TurnID: sessionID + "-turn", Input: model.TextMessage(model.RoleUser, "write"), Scope: tool.Scope{UserID: "user"}})
	if err != nil || result.Termination != agentruntime.TerminationPendingApproval {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	catalog, _ = tool.NewCatalog(item)
	runner, err = agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: jsonbApprovalModel{}, Tools: catalog, Transcript: store, Policy: Policy{Allowed: map[string]bool{"write": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ResolveApproval(ctx, sessionID, agentruntime.ApprovalResolution{TurnID: sessionID + "-turn", ToolCallID: "call", UserID: "user", Approved: true}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("executions=%d", calls)
	}
}

type jsonbApprovalModel struct{}

func (jsonbApprovalModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{"amount":1000}`)}}}}, FinishReason: model.FinishToolCalls}, nil
}

type jsonbApprovalTool struct{ calls *int }

func (jsonbApprovalTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "write", Exposure: tool.ExposureDeferred, Effects: []tool.Effect{tool.EffectExternalWrite}, InputSchema: json.RawMessage(`{"type":"object","properties":{"amount":{"type":"number","minimum":1e3}},"required":["amount"]}`)}
}
func (t jsonbApprovalTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	*t.calls++
	return tool.TextResult("done"), nil
}

func TestIntegrationCompletedToolBindingAfterJSONBRoundTrip(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, test := range []struct {
		name, args string
		wantError  bool
	}{
		{"equivalent", `{"n":9007199254740993.0,"amount":1000}`, false},
		{"different integer", `{"n":9007199254740992,"amount":1000}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionID := fmt.Sprintf("jsonb-tool-binding-%d", time.Now().UnixNano())
			turnID := sessionID + "-turn"
			defer func() {
				pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
			}()
			store := PGTranscript{Pool: pool}
			original := tool.Call{ID: "call", Name: "write", Arguments: json.RawMessage(`{"amount":1e3,"n":9007199254740993}`), Scope: tool.Scope{SessionID: sessionID, TurnID: turnID}}
			receipt := tool.TextResult("already executed")
			receipt.Usage = &model.Usage{InputTokens: 17, Reported: true}
			for i, event := range []transcript.Event{{Type: transcript.SessionStarted}, {Type: transcript.TurnStarted}, {Type: transcript.ToolCallCompleted, ToolCall: &original, ToolResult: &receipt}} {
				event.ID = fmt.Sprintf("%s-%d", sessionID, i)
				event.SessionID = sessionID
				event.TurnID = turnID
				event.Timestamp = time.Now()
				if _, err := store.Append(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			catalog, _ := tool.NewCatalog(jsonbApprovalTool{calls: &writes})
			if err := catalog.Activate(sessionID, "write"); err != nil {
				t.Fatal(err)
			}
			client := &jsonbReplayModel{call: model.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(test.args)}}
			runner, err := agentruntime.New(agentruntime.Config{Model: "integration"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store, Policy: tool.AllowAllPolicy{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(ctx, agentruntime.TurnRequest{SessionID: sessionID, TurnID: turnID, Input: model.TextMessage(model.RoleUser, "continue")})
			if test.wantError && model.ErrorKindOf(err) != model.ErrorProtocol {
				t.Fatalf("accepted retargeted JSONB call: %v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("equivalent JSONB call rejected: %v", err)
			}
			if writes != 0 || result.Usage.InputTokens != 17 {
				t.Fatalf("replayed side effect/accounting: writes=%d usage=%+v", writes, result.Usage)
			}
			replay, replayErr := runner.RunTurn(ctx, agentruntime.TurnRequest{SessionID: sessionID, TurnID: turnID})
			if replay.Usage.InputTokens != 17 || (replayErr != nil) != test.wantError {
				t.Fatalf("terminal replay differs: %+v err=%v", replay, replayErr)
			}
		})
	}
}

type jsonbReplayModel struct {
	call      model.ToolCall
	responded bool
}

func (m *jsonbReplayModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	if m.responded {
		return model.Response{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}, nil
	}
	m.responded = true
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &m.call}}}, FinishReason: model.FinishToolCalls}, nil
}
