package local

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestProjectApprovalPersistsWithoutTouchingWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "approvals.json")
	request := tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "exec"}, Call: tool.Call{Name: "exec", Arguments: json.RawMessage(`{"argv":["curl","example.com"],"network":true}`)}}
	decision := tool.Decision{Type: tool.DecisionRequireApproval, Rule: "network"}
	called := 0
	first := &ScopedApprover{Project: "/repo", Path: path, Prompt: func(context.Context, tool.PolicyRequest, tool.Decision) (ApprovalScope, error) {
		called++
		return ApprovalProject, nil
	}}
	approved, err := first.Approve(context.Background(), request, decision)
	if err != nil || !approved {
		t.Fatalf("approved=%v err=%v", approved, err)
	}
	second := &ScopedApprover{Project: "/repo", Path: path, Prompt: func(context.Context, tool.PolicyRequest, tool.Decision) (ApprovalScope, error) {
		called++
		return ApprovalDeny, nil
	}}
	approved, err = second.Approve(context.Background(), request, decision)
	if err != nil || !approved || called != 1 {
		t.Fatalf("approved=%v called=%d err=%v", approved, called, err)
	}
}
