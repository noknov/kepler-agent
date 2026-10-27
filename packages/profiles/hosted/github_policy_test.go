package hosted

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
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
