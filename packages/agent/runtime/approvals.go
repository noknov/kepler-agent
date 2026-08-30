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
	events, err := r.deps.Transcript.Load(ctx, sessionID, 0)
	if err != nil {
		return err
	}
	var call *tool.Call
	for index := range events {
		event := events[index]
		if event.TurnID != resolution.TurnID || event.ToolCall == nil || event.ToolCall.ID != resolution.ToolCallID {
			continue
		}
		switch event.Type {
		case transcript.ApprovalRequested:
			copyCall := *event.ToolCall
			call = &copyCall
		case transcript.ApprovalResolved:
			return fmt.Errorf("approval has already been resolved")
		}
	}
	if call == nil {
		return fmt.Errorf("pending approval was not found")
	}
	if call.Scope.UserID != resolution.UserID {
		return fmt.Errorf("only the requesting user may resolve this approval")
	}
	item, ok := r.deps.Tools.GetActive(sessionID, call.Name)
	if !ok {
		return fmt.Errorf("approved tool %q is unavailable", call.Name)
	}
	descriptor := item.Descriptor()
	decision, err := r.deps.Policy.Decide(ctx, tool.PolicyRequest{Descriptor: descriptor, Call: *call})
	if err != nil {
		return err
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
