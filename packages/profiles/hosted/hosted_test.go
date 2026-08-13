package hosted

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

func TestPolicyAllowsReadsAndOperatorControlledWrites(t *testing.T) {
	policy := Policy{Allowed: map[string]bool{"write_file": true}}
	read, _ := policy.Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "read_file", Effects: []tool.Effect{tool.EffectRead}}, Call: tool.Call{Name: "read_file"}})
	write, _ := policy.Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "write_file", Effects: []tool.Effect{tool.EffectWorkspaceWrite}}, Call: tool.Call{Name: "write_file"}})
	unknownWrite, _ := policy.Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: "other", Effects: []tool.Effect{tool.EffectExternalWrite}}, Call: tool.Call{Name: "other"}})
	if read.Type != tool.DecisionAllow || write.Type != tool.DecisionAllow || unknownWrite.Type != tool.DecisionDeny {
		t.Fatalf("read=%+v write=%+v unknownWrite=%+v", read, write, unknownWrite)
	}
}
