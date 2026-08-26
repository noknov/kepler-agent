package local

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// WorkspacePolicy allows reads and writes enforced by the local sandbox, and
// asks before an argv execution receives network access.
type WorkspacePolicy struct{}

func (WorkspacePolicy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	execNetwork := false
	if request.Call.Name == "exec" {
		var arguments struct {
			Network bool `json:"network"`
		}
		if err := json.Unmarshal(request.Call.Arguments, &arguments); err != nil {
			return tool.Decision{Type: tool.DecisionDeny, Reason: "cannot determine exec network access from invalid arguments", Rule: "network"}, nil
		}
		execNetwork = arguments.Network
	}
	for _, effect := range request.Descriptor.Effects {
		if effect == tool.EffectExternalWrite || effect == tool.EffectPrivileged {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "tool affects resources outside the local workspace"}, nil
		}
		if effect == tool.EffectNetwork && (request.Call.Name != "exec" || execNetwork) {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "tool requests network access", Rule: "network"}, nil
		}
	}
	return tool.Decision{Type: tool.DecisionAllow}, nil
}
