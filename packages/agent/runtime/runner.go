package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var errPendingApproval = errors.New("tool call is waiting for approval")
var runtimeTracer = otel.Tracer("github.com/noknov/kepler-agent/agent/runtime")

func (r *Runtime) RunTurn(ctx context.Context, request TurnRequest) (TurnResult, error) {
	if request.SessionID == "" {
		request.SessionID = r.deps.IDs.New("ses")
	}
	if request.TurnID == "" {
		request.TurnID = r.deps.IDs.New("turn")
	}
	ctx, span := runtimeTracer.Start(ctx, "agent.turn", trace.WithAttributes(
		attribute.String("agent.session.id", request.SessionID),
		attribute.String("agent.turn.id", request.TurnID),
	))
	defer span.End()
	if request.Input.Role == "" {
		request.Input.Role = model.RoleUser
	}
	if request.Input.Role != model.RoleUser {
		return TurnResult{}, fmt.Errorf("turn input must have user role")
	}
	if request.Input.ID == "" {
		request.Input.ID = "input:" + request.TurnID
	}
	request.Scope.SessionID = request.SessionID
	request.Scope.TurnID = request.TurnID
	unlock := r.lockSession(request.SessionID)
	defer unlock()
	// Circuit state is scoped to one live turn. Do not retain it for abandoned
	// sessions if an adapter panics or returns before terminal bookkeeping.
	defer r.clearCircuit(request.SessionID)
	defer closeInputSource(request.Steering)
	defer r.deps.Tools.EndTurn(request.SessionID, request.TurnID)

	result := TurnResult{SessionID: request.SessionID, TurnID: request.TurnID}
	events, err := r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return result, err
	}
	if replayed, replayErr, ok := completedTurn(events, request.TurnID); ok {
		return replayed, replayErr
	}
	if len(events) == 0 {
		if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, Type: transcript.SessionStarted}); err != nil {
			return result, err
		}
		for index := range request.History {
			message := request.History[index].WithoutImages()
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
	modelName := request.Model
	if modelName == "" {
		modelName = r.config.Model
	}
	turnMetadata := map[string]any{"user_id": request.Scope.UserID, "workspace": request.Scope.Workspace, "scope": request.Scope.Values, "model": modelName}
	if request.Parent != nil {
		turnMetadata["parent"] = request.Parent
	}
	turnMetadataJSON, _ := json.Marshal(turnMetadata)
	if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.TurnStarted, Status: "running", Metadata: turnMetadataJSON}); err != nil {
		return result, err
	}
	durableInput := durableUserInput(request.Input)
	if _, err = r.record(ctx, transcript.Event{SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.UserInput, Message: &durableInput}); err != nil {
		return result, err
	}

	composition, err := prompt.Compose(request.Prompt)
	if err != nil {
		return r.failTurn(ctx, result, err)
	}
	system := model.TextMessage(model.RoleSystem, composition.Content)
	for step := 1; step <= r.config.MaxSteps; step++ {
		result.Steps = step
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
				continue
			}
			closeInputSource(request.Steering)
			if count, appendErr := r.appendSteeringCount(ctx, request); appendErr != nil {
				return r.failTurn(ctx, result, appendErr)
			} else if count > 0 {
				continue
			}
			result.Message = response.Message
			return r.finishTurn(ctx, result, response.Message, TerminationCompleted, nil)
		}
		outcome, err := r.executeTools(ctx, request, calls)
		if errors.Is(err, errPendingApproval) {
			return r.finishTurn(ctx, result, response.Message, TerminationPendingApproval, err)
		}
		if err != nil {
			return r.failTurn(ctx, result, err)
		}
		if outcome.pending != nil {
			return r.finishTurn(ctx, result, *outcome.pending, TerminationPendingInput, nil)
		}
	}
	return r.finishTurn(ctx, result, result.Message, TerminationMaxSteps, errors.New("tool step limit reached"))
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
	result.Termination = TerminationReason(terminal.Status)
	if result.Termination == "" {
		result.Termination = TerminationCompleted
	}
	if terminal.Type == transcript.TurnFailed || terminal.Type == transcript.TurnCanceled {
		return result, errors.New(terminal.Error), true
	}
	return result, nil, true
}

func (r *Runtime) projectContext(ctx context.Context, request TurnRequest, system model.Message) (Projection, error) {
	events, err := r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return Projection{}, err
	}
	projection, err := r.deps.Projector.Project(ctx, events, system)
	if err != nil {
		return Projection{}, err
	}
	if len(projection.Dropped) == 0 || r.deps.Compactor == nil {
		projection.Messages = overlayEnvironment(overlayCurrentInput(projection, request.Input).Messages, r.deps.Environment)
		return projection, nil
	}
	targetTokens := 4_096
	if smallContextTarget := r.config.Context.MaxTokens / 8; smallContextTarget > 0 && smallContextTarget < targetTokens {
		targetTokens = smallContextTarget
	}
	if targetTokens < 512 {
		targetTokens = 512
	}
	summary, err := r.deps.Compactor.Compact(ctx, projection.Dropped, targetTokens)
	if err != nil {
		return Projection{}, err
	}
	metadata, _ := json.Marshal(map[string]uint64{"covers_through": projection.CoversThrough})
	if _, err = r.record(ctx, transcript.Event{
		SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.CompactionCreated,
		Message: &summary, Metadata: metadata,
	}); err != nil {
		return Projection{}, err
	}
	events, err = r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return Projection{}, err
	}
	projection, err = r.deps.Projector.Project(ctx, events, system)
	if err != nil {
		return Projection{}, err
	}
	projection.Messages = overlayEnvironment(overlayCurrentInput(projection, request.Input).Messages, r.deps.Environment)
	return projection, err
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

func overlayCurrentInput(projection Projection, current model.Message) Projection {
	if current.ID == "" {
		return projection
	}
	for index := len(projection.Messages) - 1; index >= 0; index-- {
		if projection.Messages[index].ID == current.ID {
			projection.Messages[index] = current
			projection.EstimatedTokens = EstimateTokens(projection.Messages)
			break
		}
	}
	return projection
}

func (r *Runtime) generate(ctx context.Context, turn TurnRequest, messages []model.Message) (model.Response, error) {
	return r.generateWithTools(ctx, turn, messages, r.deps.Tools.ActiveDefinitions(turn.SessionID))
}

func (r *Runtime) generateWithTools(ctx context.Context, turn TurnRequest, messages []model.Message, definitions []model.ToolDefinition) (model.Response, error) {
	modelName := turn.Model
	if modelName == "" {
		modelName = r.config.Model
	}
	request := model.Request{
		Model: modelName, Messages: messages, Tools: definitions,
		ReasoningEffort: r.config.ReasoningEffort, Temperature: r.config.Temperature, MaxOutputTokens: r.config.MaxOutputTokens,
		Metadata: map[string]string{"session_id": turn.SessionID, "turn_id": turn.TurnID},
	}
	ctx = model.WithAttemptObserver(ctx, func(attempt model.Attempt) {
		metadata, _ := json.Marshal(map[string]any{"attempt": attempt.Number, "provider": attempt.Provider, "model": attempt.Model, "fallback": attempt.Fallback, "outcome": attempt.Outcome, "remaining_ms": attempt.Remaining.Milliseconds(), "kind": model.ErrorKindOf(attempt.Error)})
		event := transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelRequested, Status: attempt.Outcome, Metadata: metadata}
		if attempt.Error != nil {
			event.Type, event.Error = transcript.ModelFailed, attempt.Error.Error()
		}
		if attempt.Outcome == "completed" {
			event.Type = transcript.ModelCompleted
		}
		_, _ = r.record(context.WithoutCancel(ctx), event)
	})
	var lastErr error
	for attempt := 0; attempt <= r.config.MaxModelRetries; attempt++ {
		metadata, _ := json.Marshal(map[string]any{"attempt": attempt + 1, "model": request.Model})
		if _, err := r.record(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelRequested, Metadata: metadata}); err != nil {
			return model.Response{}, err
		}
		response, err := r.generateAttempt(ctx, turn, request, attempt+1)
		if err == nil {
			completed, _ := json.Marshal(map[string]any{"attempt": attempt + 1, "model": request.Model, "finish_reason": response.FinishReason, "usage": response.Usage})
			if _, recordErr := r.record(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelCompleted, Metadata: completed}); recordErr != nil {
				return model.Response{}, recordErr
			}
			return response, nil
		}
		lastErr = err
		var typed *model.Error
		failed, _ := json.Marshal(map[string]any{"attempt": attempt + 1, "model": request.Model, "kind": model.ErrorKindOf(err), "retryable": errors.As(err, &typed) && typed.Retryable})
		if _, recordErr := r.record(context.WithoutCancel(ctx), transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelFailed, Error: err.Error(), Metadata: failed}); recordErr != nil {
			return model.Response{}, recordErr
		}
		if !errors.As(err, &typed) || !typed.Retryable || attempt == r.config.MaxModelRetries {
			break
		}
		if err := r.deps.Sleep(ctx, r.config.RetryBaseDelay*time.Duration(1<<attempt)); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{}, lastErr
}

func (r *Runtime) generateAttempt(ctx context.Context, turn TurnRequest, request model.Request, attempt int) (response model.Response, err error) {
	ctx, span := runtimeTracer.Start(ctx, "model.generate", trace.WithAttributes(
		attribute.String("gen_ai.request.model", request.Model),
		attribute.Int("agent.model.attempt", attempt),
	))
	defer func() {
		span.SetAttributes(
			attribute.Int64("gen_ai.usage.input_tokens", response.Usage.InputTokens),
			attribute.Int64("gen_ai.usage.output_tokens", response.Usage.OutputTokens),
		)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "model request failed")
		}
		span.End()
	}()
	return r.deps.Model.Generate(ctx, request, func(event model.StreamEvent) error {
		// Stream deltas are presentation events, not canonical durable facts.
		// Persisting each delta makes PostgreSQL/JSONL cost proportional to token
		// count. The final assistant message and model lifecycle remain durable.
		r.emit(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelStreamed, Model: &event})
		return ctx.Err()
	})
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
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadTokens += usage.CacheReadTokens
	total.CacheCreatedTokens += usage.CacheCreatedTokens
	total.CacheTokensIncludedInInput = total.CacheTokensIncludedInInput || usage.CacheTokensIncludedInInput
}

func (r *Runtime) record(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if event.ID == "" {
		event.ID = r.deps.IDs.New("evt")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = r.deps.Clock()
	}
	stored, err := r.deps.Transcript.Append(ctx, event)
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

func (r *Runtime) finishTurn(ctx context.Context, result TurnResult, message model.Message, reason TerminationReason, err error) (TurnResult, error) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("agent.termination", string(reason)), attribute.Int("agent.steps", result.Steps))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, string(reason))
	}
	result.Message = message
	result.Termination = reason
	eventType := transcript.TurnCompleted
	status := string(reason)
	if err != nil {
		eventType = transcript.TurnFailed
	}
	if reason == TerminationCanceled {
		eventType = transcript.TurnCanceled
	}
	event := transcript.Event{SessionID: result.SessionID, TurnID: result.TurnID, Type: eventType, Status: status}
	if err != nil {
		event.Error = err.Error()
	}
	_, recordErr := r.record(context.WithoutCancel(ctx), event)
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
