package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type approvalState struct {
	call                                  *tool.Call
	requested                             approvalRequestMetadata
	resolved, completed, resolvedApproved bool
}

func approvalPrincipal(decision tool.Decision, requester string) string {
	if decision.ApproverUserID != "" {
		return decision.ApproverUserID
	}
	return requester
}

// ApprovalScope authenticates a control against its durable request before an
// adapter admits it to a queue. ResolveApproval repeats this check under lease.
// The returned scope belongs to the requester, never to the approving principal.
func (r *Runtime) ApprovalScope(ctx context.Context, sessionID string, resolution ApprovalResolution) (tool.Scope, error) {
	state, err := r.loadApprovalState(ctx, sessionID, resolution)
	if err != nil {
		return tool.Scope{}, err
	}
	scope := state.call.Scope
	scope.Values = maps.Clone(scope.Values)
	return scope, nil
}

func (r *Runtime) loadApprovalState(ctx context.Context, sessionID string, resolution ApprovalResolution) (approvalState, error) {
	var state approvalState
	if sessionID == "" || resolution.TurnID == "" || resolution.ToolCallID == "" || resolution.UserID == "" {
		return state, fmt.Errorf("approval session, turn, tool call, and user are required")
	}
	events, err := r.deps.Transcript.Load(ctx, sessionID, 0)
	if err != nil {
		return state, err
	}
	for _, event := range events {
		if event.TurnID != resolution.TurnID || event.ToolCall == nil || event.ToolCall.ID != resolution.ToolCallID {
			continue
		}
		switch event.Type {
		case transcript.ApprovalRequested:
			copyCall := *event.ToolCall
			state.call = &copyCall
			if len(event.Metadata) > 0 {
				if err := json.Unmarshal(event.Metadata, &state.requested); err != nil {
					return state, err
				}
			}
		case transcript.ApprovalResolved:
			var metadata struct {
				Approved bool `json:"approved"`
			}
			if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
				return state, err
			}
			state.resolved, state.resolvedApproved = true, metadata.Approved
		case transcript.ToolCallCompleted, transcript.ToolCallFailed:
			state.completed = true
		}
	}
	if state.call == nil {
		return state, fmt.Errorf("pending approval was not found")
	}
	call := state.call
	if approvalPrincipal(state.requested.Decision, call.Scope.UserID) != resolution.UserID {
		return state, fmt.Errorf("only the designated approving user may resolve this approval")
	}
	if (call.Scope.SessionID != "" && call.Scope.SessionID != sessionID) || (call.Scope.TurnID != "" && call.Scope.TurnID != resolution.TurnID) {
		return state, fmt.Errorf("approval scope does not match its durable turn")
	}
	return state, nil
}
