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

func TestGitHubApprovalRequiresOperatorAllowlistAndSharedChannel(t *testing.T) {
	for _, actor := range []string{"requester", "owner", "other-allowed-user"} {
		for _, kind := range []string{"channel", "group", "im", "mpim", ""} {
			for _, allowed := range []bool{false, true} {
				cfg := config.Config{}
				if allowed {
					cfg.Tools.AllowedWriteTools = []string{"github-approve_pr"}
				}
				request := tool.PolicyRequest{Descriptor: (github.ApprovePRTool{}).Descriptor(), Call: tool.Call{Name: "github-approve_pr", Scope: tool.Scope{UserID: actor, Values: map[string]string{"conversation_type": kind}}}}
				decision, err := hostedPolicy(cfg).Decide(t.Context(), request)
				want := tool.DecisionDeny
				if allowed && (kind == "channel" || kind == "group") {
					want = tool.DecisionRequireApproval
				}
				if err != nil || decision.Type != want {
					t.Fatalf("actor=%s kind=%s allowed=%v decision=%+v err=%v", actor, kind, allowed, decision, err)
				}
			}
		}
	}
}
