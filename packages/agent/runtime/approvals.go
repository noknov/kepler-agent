package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// ApprovalResolution identifies one immutable tool call previously stopped for
// user confirmation. Callers must serialize resolutions for a session.
type ApprovalResolution struct {
	TurnID     string
	ToolCallID string
	Approved   bool
	UserID     string
}

// ResolveApproval records a decision and, when approved, executes the exact
// call that requested approval. It never accepts replacement tool arguments
// from the user interface or a subsequent model turn.
func (r *Runtime) ResolveApproval(ctx context.Context, sessionID string, resolution ApprovalResolution) error {
	if sessionID == "" || resolution.TurnID == "" || resolution.ToolCallID == "" || resolution.UserID == "" {
		return fmt.Errorf("approval session, turn, tool call, and user are required")
	}
	// Approval resolution is another turn mutation: it appends a decision and
	// can execute an external write. Serialize it with RunTurn so two local
	// clients cannot both observe an unresolved approval and dispatch the same
	// call. Hosted runtimes provide a distributed lease through Dependencies.
	ctx, unlock, err := r.acquireSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := (ApprovalAuthority{Transcript: r.deps.Transcript}).loadApprovalState(ctx, sessionID, resolution)
	if err != nil {
		return err
	}
	call, requested := state.call, state.requested
	if state.resolved {
		if state.completed && state.resolvedApproved == resolution.Approved {
			return nil
		}
		return fmt.Errorf("approval was resolved but its tool result is incomplete or differs")
	}
	var item tool.Tool
	var descriptor tool.Descriptor
	var decision tool.Decision
	if resolution.Approved {
		var ok bool
		item, ok = r.deps.Tools.Get(call.Name)
		if !ok || item.Descriptor().Exposure == tool.ExposureDisabled {
			return fmt.Errorf("approved tool %q is unavailable", call.Name)
		}
		descriptor, err = tool.DescribeCall(item, *call)
		if err != nil {
			return err
		}
		// Deferred exposure is temporary discovery state, not authorization. A
		// persisted descriptor binds the approval to the registered call contract.
		if requested.Descriptor != nil {
			if !sameApprovalContract(*requested.Descriptor, descriptor) {
				return fmt.Errorf("approved tool %q contract changed; request a new approval", call.Name)
			}
		} else if _, active := r.deps.Tools.GetActive(sessionID, call.Name); !active {
			return fmt.Errorf("legacy approval has no durable binding for deferred tool %q", call.Name)
		}
		if len(call.Arguments) > 0 && !json.Valid(call.Arguments) {
			return fmt.Errorf("approved tool arguments are invalid")
		}
		decision, err = r.deps.Policy.Decide(ctx, tool.PolicyRequest{Descriptor: descriptor, Call: *call})
		if err != nil {
			return err
		}
		if decision.Type != tool.DecisionAllow && decision.Type != tool.DecisionRequireApproval && decision.Type != tool.DecisionDeny {
			return fmt.Errorf("unsupported approval policy decision %q", decision.Type)
		}
	}
	metadata, _ := json.Marshal(map[string]any{"approved": resolution.Approved, "resolved_by": resolution.UserID})
	if _, err := r.record(ctx, transcript.Event{SessionID: sessionID, TurnID: resolution.TurnID, Type: transcript.ApprovalResolved, ToolCall: call, Metadata: metadata}); err != nil {
		return err
	}
	if !resolution.Approved || decision.Type == tool.DecisionDeny {
		message, code := "User declined this tool call.", "approval_declined"
		if decision.Type == tool.DecisionDeny {
			message, code = "Tool call denied by policy: "+decision.Reason, "policy_denied"
		}
		result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: message}}, IsError: true, ErrorCode: code}
		_, err := r.recordToolResults(ctx, TurnRequest{SessionID: sessionID, TurnID: resolution.TurnID, Scope: call.Scope}, []preparedCall{{call: *call, item: item, descriptor: descriptor, result: &result}})
		return err
	}
	prepared := preparedCall{call: *call, item: item, descriptor: descriptor}
	request := TurnRequest{SessionID: sessionID, TurnID: resolution.TurnID, Scope: call.Scope}
	r.runPreparedTool(ctx, request, &prepared)
	_, err = r.recordToolResults(ctx, request, []preparedCall{prepared})
	return err
}
