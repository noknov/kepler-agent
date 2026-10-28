package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type designatedPolicy struct{ user string }

func (p *designatedPolicy) Decide(context.Context, tool.PolicyRequest) (tool.Decision, error) {
	return tool.Decision{Type: tool.DecisionRequireApproval, ApproverUserID: p.user}, nil
}

func TestDesignatedApprovalPreservesRequesterAndRejectsOtherPrincipals(t *testing.T) {
	ctx := context.Background()
	for _, approved := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline", true: "approve"}[approved], func(t *testing.T) {
			calls := 0
			catalog, _ := tool.NewCatalog(countingWriteTool{calls: &calls})
			client := &scriptedModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls}}}
			policy := &designatedPolicy{user: "owner"}
			store := transcript.NewMemoryStore()
			runner, err := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Policy: policy, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(ctx, TurnRequest{SessionID: "session", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "write"), Scope: tool.Scope{UserID: "requester", Values: map[string]string{"team_id": "team"}}})
			if err != nil || result.Termination != TerminationPendingApproval || calls != 0 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
			}
			resolution := ApprovalResolution{TurnID: "turn", ToolCallID: "call", Approved: approved}
			for _, actor := range []string{"requester", "other"} {
				resolution.UserID = actor
				if _, err := runner.ApprovalScope(ctx, "session", resolution); err == nil {
					t.Fatalf("admitted %s", actor)
				}
				if err := runner.ResolveApproval(ctx, "session", resolution); err == nil {
					t.Fatalf("executed for %s", actor)
				}
			}
			resolution.UserID = "owner"
			scope, err := runner.ApprovalScope(ctx, "session", resolution)
			if err != nil || scope.UserID != "requester" {
				t.Fatalf("scope=%+v err=%v", scope, err)
			}
			scope.Values["team_id"] = "changed"
			again, _ := runner.ApprovalScope(ctx, "session", resolution)
			if again.Values["team_id"] != "team" {
				t.Fatal("scope accessor mutated durable authority")
			}
			if err := runner.ResolveApproval(ctx, "session", resolution); err != nil {
				t.Fatal(err)
			}
			if err := runner.ResolveApproval(ctx, "session", resolution); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if approved {
				expected = 1
			}
			if calls != expected {
				t.Fatalf("writes=%d want %d", calls, expected)
			}
			events, _ := store.Load(ctx, "session", 0)
			for _, event := range events {
				if event.Type == transcript.ToolCallCompleted || event.Type == transcript.ToolCallFailed {
					if event.ToolCall.Scope.UserID != "requester" {
						t.Fatal("approver replaced requester scope")
					}
				}
			}
		})
	}
}

func TestDesignatedApprovalRejectsAuthorityRotation(t *testing.T) {
	ctx := context.Background()
	calls := 0
	item := countingWriteTool{calls: &calls}
	catalog, _ := tool.NewCatalog(item)
	store := transcript.NewMemoryStore()
	descriptor := item.Descriptor()
	metadata, _ := json.Marshal(approvalRequestMetadata{Decision: tool.Decision{Type: tool.DecisionRequireApproval, ApproverUserID: "old-owner"}, Descriptor: &descriptor})
	call := tool.Call{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`), Scope: tool.Scope{UserID: "requester"}}
	_, _ = store.Append(ctx, transcript.Event{SessionID: "session", TurnID: "turn", Type: transcript.ApprovalRequested, ToolCall: &call, Metadata: metadata})
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store, Policy: &designatedPolicy{user: "new-owner"}})
	if err := runner.ResolveApproval(ctx, "session", ApprovalResolution{TurnID: "turn", ToolCallID: "call", Approved: true, UserID: "old-owner"}); err == nil || !strings.Contains(err.Error(), "approval authority changed") {
		t.Fatalf("expected authority-rotation rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatal("write executed after authority rotation")
	}
}
