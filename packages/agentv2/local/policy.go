package local

import (
	"context"
	"encoding/json"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
)

// WorkspacePolicy allows reads and writes enforced by the local sandbox, and
// asks before a shell call receives network access.
type WorkspacePolicy struct{}

func (WorkspacePolicy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	for _, effect := range request.Descriptor.Effects {
		if effect == tool.EffectExternalWrite || effect == tool.EffectPrivileged {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "tool affects resources outside the local workspace"}, nil
		}
		if effect == tool.EffectNetwork && request.Call.Name != "shell" {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "tool requests network access", Rule: "network"}, nil
		}
	}
	if request.Call.Name == "shell" {
		var arguments struct {
			Network bool `json:"network"`
		}
		if json.Unmarshal(request.Call.Arguments, &arguments) == nil && arguments.Network {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "command requests network access", Rule: "network"}, nil
		}
	}
	return tool.Decision{Type: tool.DecisionAllow}, nil
}
