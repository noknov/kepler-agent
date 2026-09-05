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

type blockingModel struct{ released chan struct{} }

func (m blockingModel) Generate(ctx context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	select {
	case <-m.released:
		return model.Response{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}, nil
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
}

func TestThreadForkCopiesEvents(t *testing.T) {
	store := transcript.NewMemoryStore()
	server := New(nil, strings.NewReader(""), &bytes.Buffer{})
	initializeForTest(t, server)
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

func TestInitializeDeclaresExactProtocolCompatibility(t *testing.T) {
	var out bytes.Buffer
	server := New(nil, strings.NewReader(""), &out)
	server.handle(context.Background(), Request{ID: json.RawMessage(`1`), Method: "initialize"})
	waitForOutput(t, &out, `"minimumProtocolVersion":2`)
	if !strings.Contains(out.String(), `"maximumProtocolVersion":2`) {
		t.Fatalf("initialize=%s", out.String())
	}
}

func TestInitializeIsSingleUse(t *testing.T) {
	var out bytes.Buffer
	server := New(nil, strings.NewReader(""), &out)
	server.handle(context.Background(), Request{ID: json.RawMessage(`1`), Method: "initialize"})
	server.handle(context.Background(), Request{ID: json.RawMessage(`2`), Method: "initialize"})
	waitForOutput(t, &out, "already initialized")
}

func TestRequestsRequireInitializeHandshake(t *testing.T) {
	var out bytes.Buffer
	server := New(nil, strings.NewReader(""), &out)
	server.handle(context.Background(), Request{ID: json.RawMessage(`1`), Method: "thread/start"})
	waitForOutput(t, &out, "not initialized")
	server.handle(context.Background(), Request{ID: json.RawMessage(`2`), Method: "initialize"})
	server.handle(context.Background(), Request{Method: "initialized"})
	server.handle(context.Background(), Request{ID: json.RawMessage(`3`), Method: "thread/start", Params: mustJSON(map[string]any{})})
	waitForOutput(t, &out, `"id":3`)
}

func TestServeReturnsJSONRPCParseError(t *testing.T) {
	var out bytes.Buffer
	server := New(nil, strings.NewReader("{not-json}\n"), &out)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &out, `"code":-32700`)
}

func TestTrajectoryReturnsRedactedOperationalItems(t *testing.T) {
	store := transcript.NewMemoryStore()
	server := New(nil, strings.NewReader(""), &bytes.Buffer{})
	initializeForTest(t, server)
	server.Transcript = store
	metadata := json.RawMessage(`{"provider":"openai","secret":"no"}`)
	_, _ = store.Append(context.Background(), transcript.Event{SessionID: "ses", TurnID: "turn", Type: transcript.ContextProjected, Metadata: metadata})
	var out bytes.Buffer
	server.writer = &out
	server.handle(context.Background(), Request{ID: json.RawMessage(`1`), Method: "thread/trajectory", Params: mustJSON(map[string]any{"sessionId": "ses"})})
	waitForOutput(t, &out, `"provider":"openai"`)
	if !strings.Contains(out.String(), `"provider":"openai"`) || strings.Contains(out.String(), "secret") {
		t.Fatalf("trajectory response=%s", out.String())
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
	initializeForTest(t, server)
	server.Transcript = transcript.NewMemoryStore()
	ctx := context.Background()
	server.handle(ctx, Request{ID: json.RawMessage(`1`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "input": "hello"})})
	server.handle(ctx, Request{ID: json.RawMessage(`2`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "input": "again"})})
	waitForNoActiveTurns(t, server)
	if !strings.Contains(out.String(), `"turnId":"turn_`) {
		t.Fatalf("expected generated turn ids, got %s", out.String())
	}
}

func TestTurnStartRejectsWhenActiveTurnBulkheadIsFull(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	released := make(chan struct{})
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{
		Model: blockingModel{released: released}, Tools: catalog, Transcript: transcript.NewMemoryStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	server := New(runner, strings.NewReader(""), &out)
	initializeForTest(t, server)
	server.MaxActiveTurns = 1
	server.handle(context.Background(), Request{ID: json.RawMessage(`1`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "turnId": "turn_1", "input": "one"})})
	deadline := time.Now().Add(time.Second)
	for {
		server.activeMu.Lock()
		active := len(server.active)
		server.activeMu.Unlock()
		if active == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	server.handle(context.Background(), Request{ID: json.RawMessage(`2`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_2", "turnId": "turn_2", "input": "two"})})
	waitForOutput(t, &out, `"code":-32001`)
	if !strings.Contains(out.String(), `"code":-32001`) || !strings.Contains(out.String(), "server overloaded; retry later") {
		t.Fatalf("expected overload response, got %s", out.String())
	}
	close(released)
	waitForNoActiveTurns(t, server)
}

func TestThreadAllowsOnlyOneActiveTurnAndCannotForkMidTurn(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	released := make(chan struct{})
	store := transcript.NewMemoryStore()
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{
		Model: blockingModel{released: released}, Tools: catalog, Transcript: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	server := New(runner, strings.NewReader(""), &out)
	server.Transcript = store
	initializeForTest(t, server)
	server.handle(context.Background(), Request{ID: json.RawMessage(`1`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "turnId": "turn_1", "input": "one"})})
	waitForActiveTurns(t, server, 1)
	server.handle(context.Background(), Request{ID: json.RawMessage(`2`), Method: "turn/start", Params: mustJSON(map[string]any{"sessionId": "ses_1", "turnId": "turn_2", "input": "two"})})
	server.handle(context.Background(), Request{ID: json.RawMessage(`3`), Method: "thread/fork", Params: mustJSON(map[string]any{"sourceSessionId": "ses_1", "childSessionId": "ses_child"})})
	waitForOutput(t, &out, "thread already has an active turn")
	waitForOutput(t, &out, "cannot fork a thread with an active turn")
	if !strings.Contains(out.String(), "cannot fork a thread with an active turn") {
		t.Fatalf("expected explicit fork boundary, got %s", out.String())
	}
	close(released)
	waitForNoActiveTurns(t, server)
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
	initializeForTest(t, server)
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
	initializeForTest(t, server)
	server.NotifyEvent(transcript.Event{
		TurnID: "turn_1", SessionID: "ses_1", Type: transcript.ModelStreamed,
		Model: &model.StreamEvent{Type: model.StreamTextDelta, Text: "hello"},
	})
	waitForOutput(t, &out, "item/agentMessage/delta")
	if !strings.Contains(out.String(), "item/agentMessage/delta") {
		t.Fatalf("expected delta notification, got %s", out.String())
	}
}

func TestApprovalRespondUnblocks(t *testing.T) {
	server := New(nil, strings.NewReader(""), &bytes.Buffer{})
	initializeForTest(t, server)
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
	if err := server.respondApproval(ApprovalRespondParams{TurnID: "turn_1", ToolCallID: "call_1", Scope: "once"}); err != nil {
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

func initializeForTest(t *testing.T, server *Server) {
	t.Helper()
	server.handle(context.Background(), Request{ID: json.RawMessage(`0`), Method: "initialize", Params: mustJSON(InitializeParams{ClientName: "test"})})
	server.handle(context.Background(), Request{Method: "initialized"})
}

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

func waitForActiveTurns(t *testing.T, server *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		server.activeMu.Lock()
		active := len(server.active)
		server.activeMu.Unlock()
		if active == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d active app-server turns; got %d", want, active)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForOutput(t *testing.T, out *bytes.Buffer, needle string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(out.String(), needle) {
		if time.Now().After(deadline) {
			t.Fatalf("output missing %q: %s", needle, out.String())
		}
		time.Sleep(time.Millisecond)
	}
}
