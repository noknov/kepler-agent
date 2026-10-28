package hosted

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	github "github.com/noknov/kepler-agent/packages/tools/github"
)

func TestWorkflowDispatchUsesOperatorWriteAuthorization(t *testing.T) {
	descriptor := (github.DispatchWorkflowTool{}).Descriptor()
	for _, tc := range []struct {
		allowed bool
		want    tool.DecisionType
	}{{false, tool.DecisionDeny}, {true, tool.DecisionRequireApproval}} {
		policy := Policy{Allowed: map[string]bool{descriptor.Name: tc.allowed}}
		decision, err := policy.Decide(t.Context(), tool.PolicyRequest{Descriptor: descriptor, Call: tool.Call{Name: descriptor.Name}})
		if err != nil || decision.Type != tc.want {
			t.Fatalf("workflow dispatch allowed=%v decision=%+v err=%v", tc.allowed, decision, err)
		}
	}
}

func TestGitHubApprovalRequiresAllowlistAndDesignatedPrincipal(t *testing.T) {
	for _, test := range []struct {
		allowed bool
		owner   string
		want    tool.DecisionType
	}{{false, "owner", tool.DecisionDeny}, {true, "", tool.DecisionDeny}, {true, "owner", tool.DecisionRequireApproval}} {
		cfg := config.Config{}
		cfg.Integrations.GitHub.ApproverUserID = test.owner
		if test.allowed {
			cfg.Tools.AllowedWriteTools = []string{"github-approve_pr"}
		}
		request := tool.PolicyRequest{Descriptor: (github.ApprovePRTool{}).Descriptor(), Call: tool.Call{Name: "github-approve_pr", Scope: tool.Scope{UserID: "requester", Values: map[string]string{"conversation_type": "channel"}}}}
		decision, err := hostedPolicy(cfg).Decide(t.Context(), request)
		if err != nil || decision.Type != test.want {
			t.Fatalf("decision=%+v err=%v", decision, err)
		}
		if test.want == tool.DecisionRequireApproval && decision.ApproverUserID != "owner" {
			t.Fatal("requester selected approval authority")
		}
	}
}

func TestDesignatedApprovalRejectsOtherUsersPrivateConversations(t *testing.T) {
	policy := Policy{Allowed: map[string]bool{"github-approve_pr": true}, Approvers: map[string]string{"github-approve_pr": "owner"}}
	for _, test := range []struct {
		actor, kind string
		want        tool.DecisionType
	}{{"other", "im", tool.DecisionDeny}, {"other", "", tool.DecisionDeny}, {"other", "channel", tool.DecisionRequireApproval}, {"owner", "im", tool.DecisionRequireApproval}} {
		decision, err := policy.Decide(t.Context(), tool.PolicyRequest{Descriptor: (github.ApprovePRTool{}).Descriptor(), Call: tool.Call{Name: "github-approve_pr", Scope: tool.Scope{UserID: test.actor, Values: map[string]string{"conversation_type": test.kind}}}})
		if err != nil || decision.Type != test.want {
			t.Fatalf("actor=%s conversation=%s decision=%+v err=%v", test.actor, test.kind, decision, err)
		}
	}
}
