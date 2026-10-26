package runtime

import (
	"context"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// completedToolResults validates the whole batch before it becomes executable
// transcript history. An ID names one immutable call within a turn, not a slot
// that a later model response can retarget. Downstream tools can additionally use
// Call.ExecutionID where their API supports stronger idempotency guarantees.
func (r *Runtime) completedToolResults(ctx context.Context, request TurnRequest, calls []model.ToolCall) (map[string]tool.Result, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	invalid := func(message string) error { return &model.Error{Kind: model.ErrorProtocol, Message: message} }
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || seen[call.ID] {
			return nil, invalid("model tool batch contains an empty or duplicate call ID")
		}
		seen[call.ID] = true
	}
	events, err := r.turnEvents(ctx, request.SessionID)
	if err != nil {
		return nil, err
	}
	results := make(map[string]tool.Result)
	bindings := make(map[string]tool.Call)
	for _, event := range events {
		if event.TurnID != request.TurnID || event.ToolCall == nil || event.ToolResult == nil {
			continue
		}
		if event.Type != transcript.ToolCallCompleted && event.Type != transcript.ToolCallFailed {
			continue
		}
		call := *event.ToolCall
		// ExecutionID was absent in older transcripts. Session/turn plus call ID
		// already bind replay. RunTurn restores the durable authorization scope;
		// transient prompt metadata is not part of model call identity. Compare
		// arguments with exact JSON-number semantics
		// because PostgreSQL JSONB may reorder keys and normalize number spelling.
		call.ExecutionID = ""
		call.Scope = tool.Scope{}
		if previous, ok := bindings[call.ID]; ok && !sameJSONDocument(previous, call) {
			return nil, invalid("durable tool call ID has conflicting bindings")
		}
		bindings[call.ID] = call
		results[call.ID] = *event.ToolResult
	}
	for _, call := range calls {
		previous, ok := bindings[call.ID]
		if !ok {
			continue
		}
		current := tool.Call{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
		if !sameJSONDocument(previous, current) {
			return nil, invalid("model reused a completed tool call ID with a different binding")
		}
	}
	return results, nil
}
