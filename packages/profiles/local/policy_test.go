package local

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

func TestWorkspacePolicyUsesStructuredExecNetworkFlag(t *testing.T) {
	descriptor := tool.Descriptor{Name: "exec", Effects: []tool.Effect{tool.EffectWorkspaceWrite, tool.EffectNetwork}}
	for _, test := range []struct {
		name string
		args string
		want tool.DecisionType
	}{
		{name: "network denied by sandbox", args: `{"argv":["rg","needle"],"network":false}`, want: tool.DecisionAllow},
		{name: "network requested", args: `{"argv":["curl","example.com"],"network":true}`, want: tool.DecisionRequireApproval},
		{name: "invalid arguments fail closed", args: `{`, want: tool.DecisionDeny},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := (WorkspacePolicy{}).Decide(context.Background(), tool.PolicyRequest{
				Descriptor: descriptor,
				Call:       tool.Call{Name: "exec", Arguments: json.RawMessage(test.args)},
			})
			if err != nil || decision.Type != test.want {
				t.Fatalf("decision=%+v err=%v, want %s", decision, err, test.want)
			}
		})
	}
}
