package local

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestWorkspacePolicyUsesTrustedEffectsWithoutCommandTextInference(t *testing.T) {
	for _, test := range []struct {
		name    string
		effects []tool.Effect
		want    tool.DecisionType
	}{
		{"workspace write", []tool.Effect{tool.EffectWorkspaceWrite}, tool.DecisionAllow},
		{"network", []tool.Effect{tool.EffectWorkspaceWrite, tool.EffectNetwork}, tool.DecisionRequireApproval},
		{"external write", []tool.Effect{tool.EffectExternalWrite}, tool.DecisionRequireApproval},
		{"privileged", []tool.Effect{tool.EffectPrivileged}, tool.DecisionRequireApproval},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{"exec", "another-adapter"} {
				decision, err := (WorkspacePolicy{}).Decide(context.Background(), tool.PolicyRequest{Descriptor: tool.Descriptor{Name: name, Effects: test.effects}, Call: tool.Call{Name: name, Arguments: []byte(`{"argv":["rm","-rf","build"]}`)}})
				if err != nil || decision.Type != test.want {
					t.Fatalf("tool=%s decision=%+v err=%v", name, decision, err)
				}
			}
		})
	}
}
