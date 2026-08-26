package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type preparedCall struct {
	index      int
	call       tool.Call
	item       tool.Tool
	descriptor tool.Descriptor
	result     *tool.Result
}

type toolOutcome struct {
	pending *model.Message
}

func (r *Runtime) executeTools(ctx context.Context, request TurnRequest, calls []model.ToolCall) (toolOutcome, error) {
	prepared := make([]preparedCall, len(calls))
	exclusive := false
	for index, modelCall := range calls {
		call := tool.Call{ID: modelCall.ID, Name: modelCall.Name, Arguments: modelCall.Arguments, Scope: request.Scope}
		prepared[index] = preparedCall{index: index, call: call}
		item, ok := r.deps.Tools.GetActive(request.SessionID, call.Name)
		if !ok {
			result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: fmt.Sprintf("Unknown tool %q.", call.Name)}}, IsError: true, ErrorCode: "unknown_tool"}
			prepared[index].result = &result
			continue
		}
		prepared[index].item = item
		prepared[index].descriptor = item.Descriptor()
		exclusive = exclusive || prepared[index].descriptor.Exclusive
	}
	if exclusive && len(calls) != 1 {
		for index := range prepared {
			result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: "An exclusive tool must be called by itself; no tools were executed."}}, IsError: true, ErrorCode: "mixed_exclusive_tool_batch"}
			prepared[index].result = &result
		}
		return r.recordToolResults(ctx, request, prepared)
	}
	for index := range prepared {
		call := prepared[index].call
		if prepared[index].result != nil {
			continue
		}
		policyRequest := tool.PolicyRequest{Descriptor: prepared[index].descriptor, Call: call}
		decision, err := r.deps.Policy.Decide(ctx, policyRequest)
		if err != nil {
			return toolOutcome{}, err
		}
		switch decision.Type {
		case tool.DecisionAllow:
		case tool.DecisionDeny:
			result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: "Tool call denied by policy: " + decision.Reason}}, IsError: true, ErrorCode: "policy_denied"}
			prepared[index].result = &result
		case tool.DecisionRequireApproval:
			metadata, _ := json.Marshal(decision)
			if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ApprovalRequested, ToolCall: &call, Metadata: metadata}); err != nil {
				return toolOutcome{}, err
			}
			if r.deps.Approver == nil {
				return toolOutcome{}, errPendingApproval
			}
			approved, approveErr := r.deps.Approver.Approve(ctx, policyRequest, decision)
			if approveErr != nil {
				return toolOutcome{}, approveErr
			}
			approvalMetadata, _ := json.Marshal(map[string]bool{"approved": approved})
			if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ApprovalResolved, ToolCall: &call, Metadata: approvalMetadata}); err != nil {
				return toolOutcome{}, err
			}
			if !approved {
				result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: "User declined this tool call."}}, IsError: true, ErrorCode: "approval_declined"}
				prepared[index].result = &result
			}
		default:
			return toolOutcome{}, fmt.Errorf("unsupported policy decision %q", decision.Type)
		}
	}

	var wait sync.WaitGroup
	for index := range prepared {
		entry := &prepared[index]
		if entry.result != nil {
			continue
		}
		if entry.descriptor.Parallel {
			wait.Add(1)
			go func() { defer wait.Done(); r.runPreparedTool(ctx, request, entry) }()
			continue
		}
		wait.Wait()
		r.runPreparedTool(ctx, request, entry)
	}
	wait.Wait()
	limitToolResultBatch(ctx, prepared, r.config.ToolResults, r.deps.Artifacts)
	return r.recordToolResults(ctx, request, prepared)
}

func (r *Runtime) recordToolResults(ctx context.Context, request TurnRequest, prepared []preparedCall) (toolOutcome, error) {
	var pending *model.Message
	for index := range prepared {
		entry := &prepared[index]
		if entry.result == nil {
			return toolOutcome{}, errors.New("tool execution produced no result")
		}
		eventType := transcript.ToolCallCompleted
		if entry.result.IsError {
			eventType = transcript.ToolCallFailed
		}
		if _, err := r.record(ctx, transcript.Event{
			SessionID: request.SessionID, TurnID: request.TurnID, Type: eventType,
			ToolCall: &entry.call, ToolResult: entry.result,
		}); err != nil {
			return toolOutcome{}, err
		}
		if entry.result.ErrorCode == "connection_required" && r.deps.ConnectionContinuations != nil {
			provider, _ := entry.result.Metadata["provider"].(string)
			channel := entry.call.Scope.Values["channel"]
			threadTS := entry.call.Scope.Values["thread_ts"]
			if provider != "" && entry.call.Scope.UserID != "" && channel != "" {
				_ = r.deps.ConnectionContinuations.Save(ctx, ConnectionContinuation{
					UserID:    entry.call.Scope.UserID,
					Provider:  provider,
					SessionID: entry.call.Scope.SessionID,
					Channel:   channel,
					ThreadTS:  threadTS,
				})
			}
		}
		if pending == nil && entry.result.NeedsUserInput && len(entry.result.Content) > 0 {
			message := model.Message{Role: model.RoleAssistant, Content: append([]model.Content(nil), entry.result.Content...)}
			pending = &message
		}
	}
	return toolOutcome{pending: pending}, nil
}

func (r *Runtime) runPreparedTool(ctx context.Context, request TurnRequest, entry *preparedCall) {
	defer func() {
		if recovered := recover(); recovered != nil {
			entry.result = &tool.Result{
				Content:   []model.Content{{Type: model.ContentText, Text: fmt.Sprintf("Tool %q failed unexpectedly.", entry.call.Name)}},
				IsError:   true,
				ErrorCode: "tool_panic",
				Metadata:  map[string]any{"panic": fmt.Sprint(recovered)},
			}
		}
	}()
	call := entry.call
	if err := r.checkCircuit(ctx, call); err != nil {
		entry.result = &tool.Result{Content: []model.Content{{Type: model.ContentText, Text: err.Error()}}, IsError: true, ErrorCode: "circuit_breaker"}
		return
	}
	if _, err := r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ToolCallStarted, ToolCall: &call}); err != nil {
		entry.result = &tool.Result{Content: []model.Content{{Type: model.ContentText, Text: err.Error()}}, IsError: true, ErrorCode: "transcript_error"}
		return
	}
	toolCtx := ctx
	cancel := func() {}
	if entry.descriptor.Timeout > 0 {
		toolCtx, cancel = context.WithTimeout(ctx, entry.descriptor.Timeout)
	}
	defer cancel()
	toolCtx, span := runtimeTracer.Start(toolCtx, "tool.execute", trace.WithAttributes(
		attribute.String("gen_ai.tool.name", call.Name),
		attribute.String("gen_ai.tool.call.id", call.ID),
	))
	defer span.End()
	started := time.Now()
	result, err := entry.item.Execute(toolCtx, call)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool execution failed")
		result.IsError = true
		if result.ErrorCode == "" {
			result.ErrorCode = "tool_error"
		}
		if len(result.Content) == 0 {
			result.Content = []model.Content{{Type: model.ContentText, Text: err.Error()}}
		}
	}
	if !hasWireVisibleToolContent(result.Content) {
		result.IsError = true
		if result.ErrorCode == "" {
			result.ErrorCode = "empty_tool_result"
		}
		result.Content = []model.Content{{Type: model.ContentText, Text: fmt.Sprintf("Tool %q returned no output.", call.Name)}}
	}
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata["duration_ms"] = time.Since(started).Milliseconds()
	span.SetAttributes(attribute.Int64("agent.tool.duration_ms", time.Since(started).Milliseconds()), attribute.Bool("agent.tool.error", result.IsError))
	result = limitToolResult(ctx, result, call, r.config.ToolResults, r.deps.Artifacts)
	r.recordCircuit(call, result.IsError || err != nil)
	entry.result = &result
}

func hasWireVisibleToolContent(content []model.Content) bool {
	for _, block := range content {
		switch block.Type {
		case model.ContentText:
			if block.Text != "" {
				return true
			}
		case model.ContentJSON:
			if len(block.JSON) > 0 {
				return true
			}
		}
	}
	return false
}
