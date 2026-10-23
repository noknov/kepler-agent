package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var errPendingApproval = errors.New("tool call is waiting for approval")
var runtimeTracer = otel.Tracer("github.com/noknov/kepler-agent/agent/runtime")

func (r *Runtime) RunTurn(ctx context.Context, request TurnRequest) (turnResult TurnResult, turnErr error) {
	if request.SessionID == "" {
		request.SessionID = r.deps.IDs.New("ses")
	}
	if request.TurnID == "" {
		request.TurnID = r.deps.IDs.New("turn")
	}
	request.Scope.SessionID = request.SessionID
	request.Scope.TurnID = request.TurnID
	class := model.WorkInteractive
	if request.Parent != nil {
		class = model.WorkBackground
	}
	owner := request.Scope.Values["team_id"] + "/" + request.Scope.UserID
	if request.Scope.UserID == "" {
		owner = "session:" + request.SessionID
	}
	ctx = model.WithWorkload(ctx, model.Workload{Owner: owner, Class: class})
	turnAttributes := langfuseObservationAttributes(request.Scope, "agent")
	turnAttributes = append(turnAttributes,
		attribute.String("agent.session.id", request.SessionID),
		attribute.String("agent.turn.id", request.TurnID),
	)
	ctx, span := runtimeTracer.Start(ctx, "agent.turn", trace.WithAttributes(turnAttributes...))
	defer span.End()
	stats := &turnTrace{}
	ctx = context.WithValue(ctx, turnTraceKey{}, stats)
	defer func() {
		if recovered := recover(); recovered != nil {
			span.SetStatus(codes.Error, "agent panicked")
			span.SetAttributes(attribute.String("langfuse.observation.metadata.run_status", "failed"), attribute.String("langfuse.observation.metadata.error_kind", "panic"))
			panic(recovered)
		}
		r.finishTrace(span, stats, turnResult, turnErr)
	}()
	if r.deps.TraceContent != nil && span.IsRecording() {
		r.traceContent(span, "input", traceMessages([]model.Message{request.Input}))
	}
	if request.Input.Role == "" {
		request.Input.Role = model.RoleUser
	}
	if request.Input.Role != model.RoleUser {
		return TurnResult{}, fmt.Errorf("turn input must have user role")
	}
	if request.Input.ID == "" {
		request.Input.ID = "input:" + request.TurnID
	}
	ctx, unlock, err := r.acquireSession(ctx, request.SessionID)
	if err != nil {
		result := TurnResult{SessionID: request.SessionID, TurnID: request.TurnID}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Termination = TerminationCanceled
		}
		// No transcript writes before ownership: another turn may still be active.
		return result, err
	}
	defer unlock()
	// Circuit state is scoped to one live turn. Do not retain it for abandoned
	// sessions if an adapter panics or returns before terminal bookkeeping.
	defer r.clearCircuit(request.SessionID)
	defer closeInputSource(request.Steering)
	defer r.deps.Tools.EndTurn(request.SessionID, request.TurnID)

	result := TurnResult{SessionID: request.SessionID, TurnID: request.TurnID}
	events, err := r.loadTurn(ctx, request.SessionID, request.TurnID)
	if err != nil {
		return result, err
	}
	r.setEventCache(request.SessionID, events)
	defer r.clearEventCache(request.SessionID)
	if replayed, replayErr, ok := completedTurn(events, request.TurnID); ok {
		return replayed, replayErr
	}
	for _, event := range events {
		if err := r.captureUnknownEffect(ctx, event); err != nil {
			return TurnResult{SessionID: request.SessionID, TurnID: request.TurnID}, err
		}
	}
	progress, err := recoverTurnProgress(events, request.TurnID, r.config)
	if err != nil {
		return result, err
	}
	result.Steps, result.Usage = progress.Steps, progress.Usage
	ctx = model.WithBudgetState(ctx, r.config.MaxTotalTokens, progress.Budget, func(recordCtx context.Context, state model.BudgetState) error {
		cleanupCtx, cancel := r.cleanupContext(recordCtx)
		defer cancel()
		metadata, err := json.Marshal(state)
		if err != nil {
			return err
		}
		_, err = r.record(cleanupCtx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.TokenBudgetUpdated, Metadata: metadata})
		return err
	})
	ctx = model.WithUsageRecorder(ctx, func(recordCtx context.Context, usage model.AttemptUsage) error {
		cleanup, cancel := r.cleanupContext(recordCtx)
		defer cancel()
		metadata, err := json.Marshal(usage)
		if err != nil {
			return err
		}
		_, err = r.record(cleanup, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ModelUsageSettled, Metadata: metadata})
		return err
	})
	// A replay retains its original authorization and reset-generation scope;
	// refreshing transient profile state must not authorize an old pending write.
	for _, event := range events {
		if event.TurnID != request.TurnID || event.Type != transcript.TurnStarted {
			continue
		}
		if len(event.Metadata) == 0 {
			continue
		} // Legacy start events had no scope.
		var original struct {
			UserID    string            `json:"user_id"`
			Workspace string            `json:"workspace"`
			Scope     map[string]string `json:"scope"`
		}
		if err := json.Unmarshal(event.Metadata, &original); err != nil {
			return result, err
		}
		if original.UserID != "" && request.Scope.UserID != "" && original.UserID != request.Scope.UserID {
			return result, fmt.Errorf("turn belongs to a different user")
		}
		if original.Scope != nil {
			request.Scope.Values = original.Scope
		}
		if original.UserID != "" {
			request.Scope.UserID = original.UserID
		}
		if original.Workspace != "" {
			request.Scope.Workspace = original.Workspace
		}
		break
	}
	if err := r.reconcileInterruptedToolCalls(ctx, request, events); err != nil {
		return r.failTurn(ctx, result, err)
	}
	if err := r.reconcileInterruptedModelRequests(ctx, request, events); err != nil {
		return r.failTurn(ctx, result, err)
	}
	if len(events) == 0 {
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, Type: transcript.SessionStarted}); err != nil {
			return result, err
		}
		history := request.History
		if len(history) == 0 && request.HistoryLoader != nil {
			loaded, loadErr := request.HistoryLoader(ctx)
			if loadErr != nil {
				return r.failTurn(ctx, result, fmt.Errorf("load session history: %w", loadErr))
			}
			history = loaded
		}
		for index := range history {
			message := history[index].WithoutImages()
			if message.Role != model.RoleUser && message.Role != model.RoleAssistant {
				message.Role = model.RoleUser
			}
			eventType := transcript.UserInput
			if message.Role == model.RoleAssistant {
				eventType = transcript.AssistantMessage
			}
			metadata, _ := json.Marshal(map[string]string{"source": "imported_history"})
			if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, Type: eventType, Message: &message, Metadata: metadata}); err != nil {
				return result, err
			}
		}
	}
	if !hasTurnEvent(events, request.TurnID, transcript.TurnStarted) {
		modelName := request.Model
		if modelName == "" {
			modelName = r.config.Model
		}
		turnMetadata := map[string]any{"user_id": request.Scope.UserID, "workspace": request.Scope.Workspace, "scope": request.Scope.Values, "model": modelName}
		// Persist the W3C trace identity with the durable turn boundary. Event
		// projections can then link a recovered run back to the distributed trace
		// without depending on an in-memory span or a particular telemetry vendor.
		if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
			turnMetadata["trace_id"] = spanContext.TraceID().String()
			turnMetadata["span_id"] = spanContext.SpanID().String()
		}
		if request.Parent != nil {
			turnMetadata["parent"] = request.Parent
		}
		turnMetadataJSON, _ := json.Marshal(turnMetadata)
		if _, err = r.record(ctx, transcript.Event{ID: "start:" + request.TurnID, SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.TurnStarted, Status: "running", Metadata: turnMetadataJSON}); err != nil {
			return result, err
		}
	}
	// A start marker can survive a failed input write. Recover each fact
	// independently before allowing the model to run; stable IDs make an
	// ambiguous commit safe to retry as well.
	if !hasTurnEvent(events, request.TurnID, transcript.UserInput) {
		durableInput := durableUserInput(request.Input)
		if _, err = r.record(ctx, transcript.Event{ID: "input:" + request.TurnID, SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.UserInput, Message: &durableInput}); err != nil {
			return result, err
		}
	}

	if request.PrepareContext != nil {
		request, err = restorePreparedContext(request, events)
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		request, err = r.prepareContext(ctx, request)
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		metadata, _ := json.Marshal(map[string]any{"values": request.Scope.Values})
		if _, err := r.record(ctx, transcript.Event{ID: "context:" + request.TurnID, SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.TurnContextPrepared, Metadata: metadata}); err != nil {
			return r.failTurn(ctx, result, err)
		}
	}
	composition, err := prompt.Compose(request.Prompt)
	if err != nil {
		return r.failTurn(ctx, result, err)
	}
	span.SetAttributes(attribute.String("langfuse.version", composition.Hash), attribute.String("langfuse.observation.metadata.prompt_hash", composition.Hash))
	values := make(map[string]string, len(request.Scope.Values)+1)
	for key, value := range request.Scope.Values {
		values[key] = value
	}
	values["prompt_hash"] = composition.Hash
	request.Scope.Values = values
	system := model.TextMessage(model.RoleSystem, composition.Content)
	ctx = tool.WithActivationAdmission(ctx, func(admissionCtx context.Context, proposed tool.ActivationRequest) error {
		events, err := r.contextEvents(admissionCtx, request.SessionID)
		if err != nil {
			return err
		}
		// Include the acknowledgement, not just the schemas: it becomes part of
		// the latest protected exchange on the very next model request.
		var sequence uint64 = 1
		if len(events) > 0 {
			sequence = events[len(events)-1].Sequence + 1
		}
		candidate := append(append([]transcript.Event(nil), events...), transcript.Event{
			Sequence: sequence, SessionID: request.SessionID, TurnID: request.TurnID,
			Type: transcript.ToolCallCompleted, ToolCall: &proposed.Call, ToolResult: &proposed.Result,
		})
		_, err = r.deps.Projector.Project(admissionCtx, r.projectionRequest(candidate, request, system, proposed.Definitions))
		return err
	})
	for step := result.Steps + 1; r.config.MaxSteps == UnlimitedSteps || step <= r.config.MaxSteps; step++ {
		result.Steps = step
		if err := r.recordStepStarted(ctx, request, step); err != nil {
			return r.failTurn(ctx, result, err)
		}
		if err := ctx.Err(); err != nil {
			return r.cancelTurn(ctx, result, err)
		}
		if err := r.appendSteering(ctx, request); err != nil {
			return r.failTurn(ctx, result, err)
		}
		projection, err := r.projectContext(ctx, request, system)
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		metadata, _ := json.Marshal(map[string]any{
			"context_policy":   r.config.Context.Policy.WithDefaults(),
			"estimated_tokens": projection.EstimatedTokens, "message_count": len(projection.Messages),
			"prompt_hash": composition.Hash, "tool_count": len(r.deps.Tools.ActiveDefinitions(request.SessionID)),
		})
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ContextProjected, Metadata: metadata}); err != nil {
			return r.failTurn(ctx, result, err)
		}

		response, err := r.generateForStep(ctx, request, system, projection.Messages)
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		addUsage(&result.Usage, response.Usage)
		if len(response.Message.Content) == 0 {
			return r.finishTurn(ctx, result, model.Message{}, TerminationEmptyResponse, errEmptyModelResponse)
		}
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.AssistantMessage, Message: &response.Message}); err != nil {
			return r.failTurn(ctx, result, err)
		}
		result.Message = response.Message
		calls := response.Message.ToolCalls()
		if len(calls) == 0 {
			if response.FinishReason == model.FinishLength {
				return r.finishTurn(ctx, result, response.Message, TerminationOutputLimit, nil)
			}
			if count, appendErr := r.appendSteeringCount(ctx, request); appendErr != nil {
				return r.failTurn(ctx, result, appendErr)
			} else if count > 0 {
				if err := r.recordStepCompleted(ctx, request, step, "steered"); err != nil {
					return r.failTurn(ctx, result, err)
				}
				continue
			}
			closeInputSource(request.Steering)
			if count, appendErr := r.appendSteeringCount(ctx, request); appendErr != nil {
				return r.failTurn(ctx, result, appendErr)
			} else if count > 0 {
				if err := r.recordStepCompleted(ctx, request, step, "steered"); err != nil {
					return r.failTurn(ctx, result, err)
				}
				continue
			}
			result.Message = response.Message
			return r.finishTurn(ctx, result, response.Message, TerminationCompleted, nil)
		}
		outcome, err := r.executeTools(ctx, request, calls)
		addUsage(&result.Usage, outcome.usage)
		if errors.Is(err, errPendingApproval) {
			// Waiting for a user decision is a normal terminal state, not a
			// failed turn. Adapters use it to suspend their session UI.
			return r.finishTurn(ctx, result, response.Message, TerminationPendingApproval, nil)
		}
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		if outcome.pending != nil {
			if _, err := r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.AssistantMessage, Message: outcome.pending}); err != nil {
				return r.failTurn(ctx, result, err)
			}
			result.Message = *outcome.pending
			return r.finishTurn(ctx, result, *outcome.pending, TerminationPendingInput, nil)
		}
		if err := r.recordStepCompleted(ctx, request, step, "tools_completed"); err != nil {
			return r.failTurn(ctx, result, err)
		}
	}
	return r.finishTurn(ctx, result, result.Message, TerminationMaxSteps, errors.New("agent execution step limit reached"))
}

func completedTurn(events []transcript.Event, turnID string) (TurnResult, error, bool) {
	if turnID == "" {
		return TurnResult{}, nil, false
	}
	result := TurnResult{TurnID: turnID}
	var terminal *transcript.Event
	for index := range events {
		event := events[index]
		if event.TurnID != turnID {
			continue
		}
		result.SessionID = event.SessionID
		switch event.Type {
		case transcript.ModelCompleted:
			var metadata struct {
				Usage model.Usage `json:"usage"`
			}
			if json.Unmarshal(event.Metadata, &metadata) == nil {
				addUsage(&result.Usage, metadata.Usage)
			}
		case transcript.AssistantMessage:
			if event.Message != nil && len(event.Message.ToolCalls()) == 0 {
				result.Message = *event.Message
			}
		case transcript.ToolCallCompleted:
			if event.ToolResult != nil && event.ToolResult.NeedsUserInput {
				result.Message = model.Message{Role: model.RoleAssistant, Content: append([]model.Content(nil), event.ToolResult.Content...)}
			}
		case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
			copyEvent := event
			terminal = &copyEvent
		}
	}
	if terminal == nil {
		return TurnResult{}, nil, false
	}
	var final struct {
		Steps int          `json:"steps"`
		Usage *model.Usage `json:"usage"`
	}
	if json.Unmarshal(terminal.Metadata, &final) == nil {
		result.Steps = final.Steps
		if final.Usage != nil {
			result.Usage = *final.Usage
		}
	}
	result.Termination = TerminationReason(terminal.Status)
	if result.Termination == "" {
		result.Termination = TerminationCompleted
	}
	if terminal.Type == transcript.TurnFailed || terminal.Type == transcript.TurnCanceled {
		return result, errors.New(terminal.Error), true
	}
	return result, nil, true
}

func hasTurnEvent(events []transcript.Event, turnID string, kind transcript.EventType) bool {
	for _, event := range events {
		if event.TurnID == turnID && event.Type == kind {
			return true
		}
	}
	return false
}

// reconcileInterruptedToolCalls closes incomplete tool executions during a
// replay. A started call may have already produced an external side effect, so
// re-executing it is unsafe. Record a deterministic error result instead and
// let the model decide how to proceed from the uncertain state.
func (r *Runtime) reconcileInterruptedToolCalls(ctx context.Context, request TurnRequest, events []transcript.Event) error {
	started := make(map[string]tool.Call)
	pending := make(map[string]tool.Call)
	for _, event := range events {
		if event.TurnID != request.TurnID {
			continue
		}
		if event.Type == transcript.AssistantMessage && event.Message != nil {
			for _, call := range event.Message.ToolCalls() {
				pending[call.ID] = tool.Call{ID: call.ID, Name: call.Name, Arguments: call.Arguments, Scope: request.Scope}
			}
		}
		if event.ToolCall == nil {
			continue
		}
		switch event.Type {
		case transcript.ToolCallStarted:
			started[event.ToolCall.ID] = *event.ToolCall
		case transcript.ToolCallCompleted, transcript.ToolCallFailed:
			delete(started, event.ToolCall.ID)
			delete(pending, event.ToolCall.ID)
		case transcript.ApprovalRequested:
			delete(pending, event.ToolCall.ID)
		}
	}
	for id, call := range pending {
		if _, ok := started[id]; ok {
			continue
		}
		result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: "Tool call was not started before execution was interrupted. It is safe to request it again."}}, IsError: true, ErrorCode: "execution_not_started"}
		if _, err := r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ToolCallFailed, ToolCall: &call, ToolResult: &result}); err != nil {
			return err
		}
	}
	for _, call := range started {
		result := tool.Result{
			Content:   []model.Content{{Type: model.ContentText, Text: fmt.Sprintf("Tool %q was interrupted before its result was recorded and was not retried because it may have already completed.", call.Name)}},
			IsError:   true,
			ErrorCode: "execution_interrupted",
			Metadata:  map[string]any{"recovered": true},
		}
		stored, err := r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.ToolCallFailed, ToolCall: &call, ToolResult: &result})
		if err != nil {
			return err
		}
		if err := r.captureUnknownEffect(ctx, stored); err != nil {
			return err
		}
	}
	return nil
}

type modelRequestState struct {
	RequestID string `json:"request_id"`
	Attempt   int    `json:"attempt,omitempty"`
}

func modelRequestKey(state modelRequestState) string {
	if state.Attempt <= 0 {
		return state.RequestID
	}
	return fmt.Sprintf("%s:attempt:%d", state.RequestID, state.Attempt)
}

// reconcileInterruptedModelRequests makes the uncertainty caused by a process
// loss explicit. Providers do not offer a portable read-after-write API for a
// generation request, so the runtime deliberately does not guess whether the
// request reached the provider or replay its partial output.
func (r *Runtime) reconcileInterruptedModelRequests(ctx context.Context, request TurnRequest, events []transcript.Event) error {
	started := make(map[string]modelRequestState)
	settled := make(map[string]bool)
	for _, event := range events {
		if event.TurnID != request.TurnID {
			continue
		}
		var state modelRequestState
		if json.Unmarshal(event.Metadata, &state) != nil || state.RequestID == "" {
			continue
		}
		switch event.Type {
		case transcript.ModelRequestStarted:
			started[modelRequestKey(state)] = state
		case transcript.ModelCompleted, transcript.ModelFailed, transcript.ModelRequestUnknown:
			settled[modelRequestKey(state)] = true
		}
	}
	for key, state := range started {
		if settled[key] {
			continue
		}
		metadata, _ := json.Marshal(state)
		unknownID := "model-request-unknown:" + state.RequestID
		if state.Attempt > 0 {
			unknownID += fmt.Sprintf(":attempt:%d", state.Attempt)
		}
		if _, err := r.record(ctx, transcript.Event{
			ID: unknownID, SessionID: request.SessionID, TurnID: request.TurnID,
			Type: transcript.ModelRequestUnknown, Status: "unknown", Error: "model request was in flight when execution was interrupted", Metadata: metadata,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) recordStepStarted(ctx context.Context, request TurnRequest, step int) error {
	metadata, _ := json.Marshal(map[string]int{"step": step})
	_, err := r.record(ctx, transcript.Event{ID: fmt.Sprintf("step:%s:%d:started", request.TurnID, step), SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.StepStarted, Status: "running", Metadata: metadata})
	return err
}

func (r *Runtime) recordStepCompleted(ctx context.Context, request TurnRequest, step int, status string) error {
	metadata, _ := json.Marshal(map[string]int{"step": step})
	_, err := r.record(ctx, transcript.Event{ID: fmt.Sprintf("step:%s:%d:completed", request.TurnID, step), SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.StepCompleted, Status: status, Metadata: metadata})
	return err
}

func durableUserInput(message model.Message) model.Message {
	copyMessage := message
	copyMessage.Content = make([]model.Content, 0, len(message.Content))
	for _, block := range message.Content {
		if block.Type == model.ContentImage {
			copyMessage.Content = append(copyMessage.Content, model.Content{Type: model.ContentText, Text: "[image attachment was available to the model during its original turn; binary data is not retained in the transcript]"})
			continue
		}
		copyMessage.Content = append(copyMessage.Content, block)
	}
	return copyMessage
}

func (r *Runtime) appendSteering(ctx context.Context, request TurnRequest) error {
	_, err := r.appendSteeringCount(ctx, request)
	return err
}

func (r *Runtime) appendSteeringCount(ctx context.Context, request TurnRequest) (int, error) {
	if request.Steering == nil {
		return 0, nil
	}
	inputs, err := request.Steering.Claim(ctx, 100)
	if err != nil {
		return 0, err
	}
	for index := range inputs {
		message := inputs[index].Message
		if message.Role == "" {
			message.Role = model.RoleUser
		}
		eventID := "steering:" + request.TurnID + ":" + inputs[index].ID
		if _, err := r.record(ctx, transcript.Event{ID: eventID, SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.SteeringInput, Message: &message}); err != nil {
			return index, err
		}
		if err := request.Steering.Ack(ctx, inputs[index].ID); err != nil {
			return index, err
		}
	}
	return len(inputs), nil
}

func addUsage(total *model.Usage, usage model.Usage) {
	total.Reported = total.Reported || usage.Reported
	total.ReasoningTokens += usage.ReasoningTokens
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadTokens += usage.CacheReadTokens
	total.CacheCreatedTokens += usage.CacheCreatedTokens
	total.CacheTokensIncludedInInput = total.CacheTokensIncludedInInput || usage.CacheTokensIncludedInInput
}

func (r *Runtime) record(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if event.Trace == nil {
		if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
			event.Trace = &transcript.TraceContext{TraceID: spanContext.TraceID().String(), SpanID: spanContext.SpanID().String()}
		}
	}
	if event.ID == "" {
		event.ID = r.deps.IDs.New("evt")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = r.deps.Clock()
	}
	stored, err := r.deps.Transcript.Append(ctx, event)
	if err == nil {
		r.eventMu.Lock()
		if cached, ok := r.eventCache[event.SessionID]; ok {
			cached.append(stored)
		}
		r.eventMu.Unlock()
	}
	if err == nil && r.deps.Events != nil {
		r.emit(ctx, stored)
	}
	return stored, err
}

func (r *Runtime) emit(ctx context.Context, event transcript.Event) {
	if r.deps.Events == nil {
		return
	}
	if event.ID == "" {
		event.ID = r.deps.IDs.New("evt_stream")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = r.deps.Clock()
	}
	// Presentation/observability adapters are outside the canonical commit
	// path. A buggy sink must not crash a worker or roll back a stored event.
	defer func() { _ = recover() }()
	r.deps.Events.Publish(ctx, event)
}

func (r *Runtime) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), r.config.CleanupTimeout)
}

func (r *Runtime) finishTurn(ctx context.Context, result TurnResult, message model.Message, reason TerminationReason, err error) (TurnResult, error) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("agent.termination", string(reason)), attribute.Int("agent.steps", result.Steps))
	if err != nil {
		recordSpanError(span, err)
		span.SetStatus(codes.Error, string(reason))
	}
	cleanupCtx, cancel := r.cleanupContext(ctx)
	defer cancel()
	result.Message = message
	result.Termination = reason
	if result.Steps > 0 {
		if stepErr := r.recordStepCompleted(cleanupCtx, TurnRequest{SessionID: result.SessionID, TurnID: result.TurnID}, result.Steps, string(reason)); stepErr != nil && err == nil {
			err = stepErr
		}
	}
	eventType := transcript.TurnCompleted
	status := string(reason)
	if err != nil {
		eventType = transcript.TurnFailed
	}
	if reason == TerminationCanceled {
		eventType = transcript.TurnCanceled
	}
	metadata, _ := json.Marshal(map[string]any{
		"steps": result.Steps,
		"usage": result.Usage,
	})
	event := transcript.Event{SessionID: result.SessionID, TurnID: result.TurnID, Type: eventType, Status: status, Metadata: metadata}
	if err != nil {
		event.Error = err.Error()
	}
	_, recordErr := r.record(cleanupCtx, event)
	if recordErr != nil && err == nil {
		err = recordErr
	}
	r.clearCircuit(result.SessionID)
	return result, err
}

func closeInputSource(source InputSource) {
	if closable, ok := source.(interface{ Close() }); ok {
		closable.Close()
	}
}

func (r *Runtime) failTurn(ctx context.Context, result TurnResult, err error) (TurnResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return r.cancelTurn(ctx, result, err)
	}
	switch model.ErrorKindOf(err) {
	case model.ErrorBudgetExhausted:
		result.Termination = TerminationBudgetExhausted
	case model.ErrorCircuitOpen:
		result.Termination = TerminationProviderCircuit
	case model.ErrorFallbackExhausted:
		result.Termination = TerminationFallbackExhausted
	default:
		result.Termination = TerminationModelError
	}
	return r.finishTurn(ctx, result, result.Message, result.Termination, err)
}

func (r *Runtime) cancelTurn(ctx context.Context, result TurnResult, err error) (TurnResult, error) {
	result.Termination = TerminationCanceled
	return r.finishTurn(ctx, result, result.Message, result.Termination, err)
}
