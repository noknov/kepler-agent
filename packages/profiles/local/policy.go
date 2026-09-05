package local

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
)

// WorkspacePolicy allows reads and writes enforced by the local sandbox, and
// asks before an argv execution receives network access.
type WorkspacePolicy struct {
	RiskSignals safety.CommandPolicy
}

func NewWorkspacePolicy() WorkspacePolicy {
	return WorkspacePolicy{RiskSignals: safety.NewLocalCommandPolicy()}
}

func (p WorkspacePolicy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	execNetwork := false
	if request.Call.Name == "exec" {
		var arguments struct {
			Argv    []string `json:"argv"`
			Network bool     `json:"network"`
		}
		if err := json.Unmarshal(request.Call.Arguments, &arguments); err != nil {
			return tool.Decision{Type: tool.DecisionDeny, Reason: "cannot determine exec network access from invalid arguments", Rule: "network"}, nil
		}
		execNetwork = arguments.Network
		// This pattern matcher is only a conservative approval signal. The
		// structured argv boundary, workspace sandbox and approval decision remain
		// authoritative; matching never claims the command was fully understood.
		if err := p.RiskSignals.CheckArgv(arguments.Argv); err != nil {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "command matches a destructive-operation risk signal", Rule: "heuristic-command-risk"}, nil
		}
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
