package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
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
	looped  bool
	pending *model.Message
}

func (r *Runtime) executeTools(ctx context.Context, request TurnRequest, calls []model.ToolCall, repeated map[string]int) (toolOutcome, error) {
	prepared := make([]preparedCall, len(calls))
	blockedByLoop := 0
	for index, modelCall := range calls {
		call := tool.Call{ID: modelCall.ID, Name: modelCall.Name, Arguments: modelCall.Arguments, Scope: request.Scope}
		prepared[index] = preparedCall{index: index, call: call}
		key := toolCallKey(modelCall)
		repeated[key]++
		if repeated[key] > r.config.MaxRepeatedToolCalls {
			blockedByLoop++
			result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: "Repeated identical tool call blocked by loop policy."}}, IsError: true, ErrorCode: "repeated_tool_call"}
			prepared[index].result = &result
			continue
		}
		item, ok := r.deps.Tools.GetActive(request.SessionID, call.Name)
		if !ok {
			result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: fmt.Sprintf("Unknown tool %q.", call.Name)}}, IsError: true, ErrorCode: "unknown_tool"}
			prepared[index].result = &result
			continue
		}
		prepared[index].item = item
		prepared[index].descriptor = item.Descriptor()
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
		if pending == nil && entry.result.NeedsUserInput && len(entry.result.Content) > 0 {
			message := model.Message{Role: model.RoleAssistant, Content: append([]model.Content(nil), entry.result.Content...)}
			pending = &message
		}
	}
	return toolOutcome{looped: blockedByLoop == len(calls), pending: pending}, nil
}

func (r *Runtime) runPreparedTool(ctx context.Context, request TurnRequest, entry *preparedCall) {
	call := entry.call
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
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata["duration_ms"] = time.Since(started).Milliseconds()
	span.SetAttributes(attribute.Int64("agent.tool.duration_ms", time.Since(started).Milliseconds()), attribute.Bool("agent.tool.error", result.IsError))
	result = limitToolResult(toolCtx, result, call, r.config.ToolResults, r.deps.Artifacts)
	entry.result = &result
}
