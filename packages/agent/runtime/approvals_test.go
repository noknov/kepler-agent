package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type approvalPolicy struct{}

func (approvalPolicy) Decide(context.Context, tool.PolicyRequest) (tool.Decision, error) {
	return tool.Decision{Type: tool.DecisionRequireApproval, Rule: "user_confirmation"}, nil
}

type blockingApprovalWriteTool struct {
	calls   atomic.Int32
	started chan struct{}
	release <-chan struct{}
}

func (t *blockingApprovalWriteTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "write", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectExternalWrite}}
}

func (t *blockingApprovalWriteTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	t.calls.Add(1)
	t.started <- struct{}{}
	<-t.release
	return tool.TextResult("wrote"), nil
}

func TestResolveApprovalExecutesOnlyTheStoredCall(t *testing.T) {
	calls := 0
	modelClient := &scriptedModel{responses: []model.Response{{
		Message:      model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{"value":"approved"}`)}}}},
		FinishReason: model.FinishToolCalls,
	}}}
	catalog, _ := tool.NewCatalog(countingWriteTool{calls: &calls})
	store := transcript.NewMemoryStore()
	runner, err := New(Config{Model: "test"}, Dependencies{Model: modelClient, Tools: catalog, Policy: approvalPolicy{}, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "session", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "write"), Scope: tool.Scope{UserID: "U1"}})
	if err != nil || result.Termination != TerminationPendingApproval || calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if err := runner.ResolveApproval(context.Background(), "session", ApprovalResolution{TurnID: "turn", ToolCallID: "write-1", Approved: true, UserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
	if err := runner.ResolveApproval(context.Background(), "session", ApprovalResolution{TurnID: "turn", ToolCallID: "write-1", Approved: true, UserID: "U1"}); err != nil {
		t.Fatalf("idempotent duplicate resolution: %v", err)
	}
	if calls != 1 {
		t.Fatalf("duplicate resolution repeated side effect: calls=%d", calls)
	}
}

func TestResolveApprovalRejectsAnotherUser(t *testing.T) {
	call := tool.Call{ID: "write-1", Name: "write", Scope: tool.Scope{UserID: "U1"}}
	store := transcript.NewMemoryStore()
	_, _ = store.Append(context.Background(), transcript.Event{SessionID: "session", TurnID: "turn", Type: transcript.ApprovalRequested, ToolCall: &call})
	catalog, _ := tool.NewCatalog(countingWriteTool{calls: new(int)})
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
	if err := runner.ResolveApproval(context.Background(), "session", ApprovalResolution{TurnID: "turn", ToolCallID: "write-1", Approved: true, UserID: "U2"}); err == nil {
		t.Fatal("approval by another user was accepted")
	}
}

func TestResolveApprovalSerializesConcurrentResolutions(t *testing.T) {
	call := tool.Call{ID: "write-1", Name: "write", Scope: tool.Scope{SessionID: "session", TurnID: "turn", UserID: "U1"}}
	store := transcript.NewMemoryStore()
	_, _ = store.Append(context.Background(), transcript.Event{SessionID: "session", TurnID: "turn", Type: transcript.ApprovalRequested, ToolCall: &call})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	item := &blockingApprovalWriteTool{started: started, release: release}
	catalog, _ := tool.NewCatalog(item)
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Policy: approvalPolicy{}, Transcript: store})
	resolution := ApprovalResolution{TurnID: "turn", ToolCallID: "write-1", Approved: true, UserID: "U1"}
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- runner.ResolveApproval(context.Background(), "session", resolution) }()
	<-started
	go func() { second <- runner.ResolveApproval(context.Background(), "session", resolution) }()
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("idempotent concurrent resolution: %v", err)
	}
	if calls := item.calls.Load(); calls != 1 {
		t.Fatalf("tool calls = %d, want 1", calls)
	}
}

func TestPendingApprovalCompletesTheSiblingToolResults(t *testing.T) {
	calls := 0
	modelClient := &scriptedModel{responses: []model.Response{{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{
			{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{}`)}},
			{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "write-2", Name: "write", Arguments: json.RawMessage(`{}`)}},
		}},
		FinishReason: model.FinishToolCalls,
	}}}
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog(countingWriteTool{calls: &calls})
	runner, err := New(Config{}, Dependencies{Model: modelClient, Tools: catalog, Policy: approvalPolicy{}, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{
		SessionID: "approval-batch", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "write"), Scope: tool.Scope{UserID: "U1"},
	})
	if err != nil || result.Termination != TerminationPendingApproval || calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if err := runner.ResolveApproval(context.Background(), "approval-batch", ApprovalResolution{TurnID: "turn", ToolCallID: "write-1", Approved: true, UserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want one approved call", calls)
	}
	events, err := store.Load(context.Background(), "approval-batch", 0)
	if err != nil {
		t.Fatal(err)
	}
	var firstCompleted, secondBlocked bool
	for _, event := range events {
		if event.ToolCall == nil || event.ToolResult == nil {
			continue
		}
		if event.ToolCall.ID == "write-1" && event.Type == transcript.ToolCallCompleted {
			firstCompleted = true
		}
		if event.ToolCall.ID == "write-2" && event.ToolResult.ErrorCode == "approval_batch_blocked" {
			secondBlocked = true
		}
	}
	if !firstCompleted || !secondBlocked {
		t.Fatalf("approval batch results missing: completed=%v blocked=%v events=%+v", firstCompleted, secondBlocked, events)
	}
}
