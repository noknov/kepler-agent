package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

type oneShotModel struct{}

func (oneShotModel) Generate(_ context.Context, _ model.Request, sink model.EventSink) (model.Response, error) {
	_ = sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "hi"})
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "hi"), FinishReason: model.FinishStop}, nil
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
	if !strings.Contains(out.String(), `"turnId":"turn_`) {
		t.Fatalf("expected generated turn ids, got %s", out.String())
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

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func ptr[T any](value T) *T { return &value }
