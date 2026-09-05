package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/local"
)

type oneShotModel struct{}

func (oneShotModel) Generate(_ context.Context, _ model.Request, sink model.EventSink) (model.Response, error) {
	_ = sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "hi"})
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "hi"), FinishReason: model.FinishStop}, nil
}

type deadlineProbeModel struct{ done chan error }

func (m deadlineProbeModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	<-ctx.Done()
	m.done <- ctx.Err()
	return model.Response{}, ctx.Err()
}

func TestThreadForkCopiesEvents(t *testing.T) {
	store := transcript.NewMemoryStore()
	server := New(nil, strings.NewReader(""), &bytes.Buffer{})
	server.Transcript = store
	ctx := context.Background()
	_, _ = store.Append(ctx, transcript.Event{SessionID: "ses_parent", Type: transcript.SessionStarted})
	_, _ = store.Append(ctx, transcript.Event{SessionID: "ses_parent", TurnID: "turn_1", Type: transcript.UserInput, Message: ptr(model.TextMessage(model.RoleUser, "hello"))})

	var response Response
	server.handle(ctx, Request{Method: "thread/fork", Params: mustJSON(map[string]any{"sourceSessionId": "ses_parent", "childSessionId": "ses_child"})})
	// fork is sync in handle - read from writer... server writes to buffer only on respond
	_ = response
	childEvents, err := store.Load(ctx, "ses_child", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(childEvents) != 2 {
		t.Fatalf("forked events = %d, want 2", len(childEvents))
	}
	ids := make(map[string]bool, len(childEvents))
	for _, event := range childEvents {
		if event.ID == "" || ids[event.ID] {
			t.Fatalf("forked event IDs must be unique and non-empty: %#v", childEvents)
		}
		ids[event.ID] = true
	}
}

func TestTurnStartUsesUniqueTurnIDs(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{
		Model: oneShotModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	server := New(runner, strings.NewReader(""), &out)
	server.Transcript = transcript.NewMemoryStore()
	ctx := context.Background()
	server.handle(ctx, Request{ID: json.RawMessage(`1`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "input": "hello"})})
	server.handle(ctx, Request{ID: json.RawMessage(`2`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "input": "again"})})
	waitForNoActiveTurns(t, server)
	if !strings.Contains(out.String(), `"turnId":"turn_`) {
		t.Fatalf("expected generated turn ids, got %s", out.String())
	}
}

func TestExecuteAppliesTurnTimeout(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	done := make(chan error, 1)
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{
		Model: deadlineProbeModel{done: done}, Tools: catalog, Transcript: transcript.NewMemoryStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := New(runner, strings.NewReader(""), &bytes.Buffer{})
	server.TurnTimeout = 10 * time.Millisecond
	server.execute(context.Background(), TurnStartParams{SessionID: "ses_timeout", TurnID: "turn_timeout", Input: "hello"}, &agentruntime.InputBuffer{})
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Fatalf("model context error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("turn timeout did not cancel the model")
	}
}

func TestNotifyEventMapsTextDelta(t *testing.T) {
	var out bytes.Buffer
	server := New(nil, strings.NewReader(""), &out)
	server.NotifyEvent(transcript.Event{
		TurnID: "turn_1", SessionID: "ses_1", Type: transcript.ModelStreamed,
		Model: &model.StreamEvent{Type: model.StreamTextDelta, Text: "hello"},
	})
	if !strings.Contains(out.String(), "item/agentMessage/delta") {
		t.Fatalf("expected delta notification, got %s", out.String())
	}
}

func TestApprovalRespondUnblocks(t *testing.T) {
	server := New(nil, strings.NewReader(""), &bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan local.ApprovalScope, 1)
	go func() {
		scope, err := server.waitApproval(ctx, "turn_1", "call_1")
		if err != nil {
			t.Errorf("waitApproval: %v", err)
			return
		}
		done <- scope
	}()
	deadline := time.Now().Add(time.Second)
	for {
		server.pendingMu.Lock()
		_, ready := server.pending[approvalKey("turn_1", "call_1")]
		server.pendingMu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := server.respondApproval(approvalRespondParams{TurnID: "turn_1", ToolCallID: "call_1", Scope: "once"}); err != nil {
		t.Fatal(err)
	}
	select {
	case scope := <-done:
		if scope != local.ApprovalOnce {
			t.Fatalf("scope = %q, want once", scope)
		}
	case <-time.After(time.Second):
		t.Fatal("approval was not unblocked")
	}
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func ptr[T any](value T) *T { return &value }

func waitForNoActiveTurns(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		server.activeMu.Lock()
		active := len(server.active)
		server.activeMu.Unlock()
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d active app-server turns", active)
		}
		time.Sleep(time.Millisecond)
	}
}
