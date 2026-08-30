// Package hosted provides the server-owned runtime profile. Slack, HTTP, and
// future surfaces adapt into this profile; they are not separate agent types.
package hosted

import (
	"context"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// Policy keeps the operator allowlist authoritative and requires the
// requesting user to confirm every allowed write.
type Policy struct{ Allowed map[string]bool }

func (p Policy) Decide(_ context.Context, request tool.PolicyRequest) (tool.Decision, error) {
	if request.Call.Name == "web-search" && request.Call.Scope.Values["web_search"] == "disabled" {
		return tool.Decision{Type: tool.DecisionDeny, Reason: "web search is disabled for this user"}, nil
	}
	for _, effect := range request.Descriptor.Effects {
		if (effect == tool.EffectWorkspaceWrite || effect == tool.EffectExternalWrite || effect == tool.EffectPrivileged) && !p.Allowed[request.Call.Name] {
			return tool.Decision{Type: tool.DecisionDeny, Reason: "write tool is not in the operator allowlist"}, nil
		}
		if effect == tool.EffectWorkspaceWrite || effect == tool.EffectExternalWrite || effect == tool.EffectPrivileged {
			return tool.Decision{Type: tool.DecisionRequireApproval, Reason: "this action changes data or an external service", Rule: "user_confirmation"}, nil
		}
	}
	return tool.Decision{Type: tool.DecisionAllow}, nil
}

type Agent struct {
	Runtime *agentruntime.Runtime
	Prompt  []prompt.Fragment
}
type Request struct {
	SessionID, TurnID, UserID, Workspace string
	Input                                model.Message
	History                              []model.Message
	Model                                string
	Steering                             agentruntime.InputSource
	Prompt                               []prompt.Fragment
	ScopeValues                          map[string]string
}

func (a Agent) Run(ctx context.Context, request Request) (agentruntime.TurnResult, error) {
	if a.Runtime == nil {
		return agentruntime.TurnResult{}, fmt.Errorf("hosted runtime is not configured")
	}
	if len(request.Input.Content) == 0 {
		return agentruntime.TurnResult{}, fmt.Errorf("input is empty")
	}
	request.Input.Role = model.RoleUser
	fragments := append([]prompt.Fragment(nil), a.Prompt...)
	fragments = append(fragments, request.Prompt...)
	return a.Runtime.RunTurn(ctx, agentruntime.TurnRequest{SessionID: request.SessionID, TurnID: request.TurnID, Input: request.Input, History: request.History, Prompt: fragments, Scope: tool.Scope{SessionID: request.SessionID, TurnID: request.TurnID, UserID: request.UserID, Workspace: request.Workspace, Values: request.ScopeValues}, Steering: request.Steering, Model: request.Model})
}
