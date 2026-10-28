package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestApprovalScopeRequiresRequesterAndPreservesExecutionScope(t *testing.T) {
	ctx := context.Background()
	for _, approved := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline", true: "approve"}[approved], func(t *testing.T) {
			calls := 0
			catalog, _ := tool.NewCatalog(countingWriteTool{calls: &calls})
			client := &scriptedModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls}}}
			policy := approvalPolicy{}
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
			for _, actor := range []string{"owner", "other"} {
				resolution.UserID = actor
				if _, err := (ApprovalAuthority{Transcript: store}).ApprovalScope(ctx, "session", resolution); err == nil {
					t.Fatalf("admitted %s", actor)
				}
				if err := runner.ResolveApproval(ctx, "session", resolution); err == nil {
					t.Fatalf("executed for %s", actor)
				}
			}
			resolution.UserID = "requester"
			scope, err := (ApprovalAuthority{Transcript: store}).ApprovalScope(ctx, "session", resolution)
			if err != nil || scope.UserID != "requester" {
				t.Fatalf("scope=%+v err=%v", scope, err)
			}
			scope.Values["team_id"] = "changed"
			again, _ := (ApprovalAuthority{Transcript: store}).ApprovalScope(ctx, "session", resolution)
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
			if _, err := (ApprovalAuthority{Transcript: store}).ApprovalScope(ctx, "session", resolution); err == nil {
				t.Fatal("completed approval admitted another control")
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
