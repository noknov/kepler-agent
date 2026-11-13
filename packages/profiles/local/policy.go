package local

import (
	"context"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// WorkspacePolicy relies on the sandbox for workspace confinement. The trusted
// adapter declares exact call effects before policy; command text grants no access.
type WorkspacePolicy struct{}

func (WorkspacePolicy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	for _, effect := range request.Descriptor.Effects {
		switch effect {
		case tool.EffectExternalWrite, tool.EffectPrivileged:
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "tool affects resources outside the local workspace"}, nil
		case tool.EffectNetwork:
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "tool requests network access", Rule: "network"}, nil
		}
	}
	return tool.Decision{Type: tool.DecisionAllow}, nil
}
