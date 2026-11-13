package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type continuationReceipt struct {
	cause error
	saved []ConnectionContinuation
}

func (s *continuationReceipt) Save(_ context.Context, c ConnectionContinuation) error {
	s.saved = append(s.saved, c)
	return s.cause
}

func TestConnectionHandoffFailurePreservesCompleteToolBatchAndCanRetry(t *testing.T) {
	cause := errors.New("continuation storage unavailable")
	receipt := &continuationReceipt{cause: cause}
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	runner, setupErr := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store, ConnectionContinuations: receipt})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	scope := tool.Scope{UserID: "owner", SessionID: "s", Values: map[string]string{"surface": "benchmark"}}
	waiting := tool.Result{Content: tool.TextResult("connect first").Content, IsError: true, NeedsUserInput: true, ErrorCode: "connection_required", Metadata: map[string]any{"provider": "notion", "instance_id": "named"}}
	sibling := tool.TextResult("sibling evidence")
	prepared := []preparedCall{
		{call: tool.Call{ID: "connect", Scope: scope}, result: &waiting},
		{call: tool.Call{ID: "read", Scope: scope}, result: &sibling},
	}
	request := TurnRequest{SessionID: "s", TurnID: "turn"}
	if _, err := runner.recordToolResults(t.Context(), request, prepared); !errors.Is(err, cause) {
		t.Fatalf("handoff failure was hidden: %v", err)
	}
	events, err := store.Load(t.Context(), "s", 0)
	if err != nil || len(events) != 2 || events[0].ToolCall.ID != "connect" || events[1].ToolCall.ID != "read" {
		t.Fatalf("handoff failure orphaned a sibling: %+v %v", events, err)
	}
	if len(receipt.saved) != 1 || receipt.saved[0].Scope.UserID != "owner" || receipt.saved[0].TurnID != "turn" || receipt.saved[0].Scope.Values["surface"] != "benchmark" {
		t.Fatalf("handoff was not scoped without Slack fields: %+v", receipt.saved)
	}
	receipt.cause = nil
	for i := range prepared {
		prepared[i].reused = true
	}
	if _, err := runner.recordToolResults(t.Context(), request, prepared); err != nil || len(receipt.saved) != 2 {
		t.Fatalf("replayed result could not repair the handoff: %v", err)
	}
}
