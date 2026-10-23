package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"sync/atomic"
	"time"
)

func (r *Runtime) generate(ctx context.Context, turn TurnRequest, messages []model.Message) (model.Response, error) {
	return r.generateWithTools(ctx, turn, messages, r.deps.Tools.ActiveDefinitions(turn.SessionID))
}

func (r *Runtime) generateWithTools(ctx context.Context, turn TurnRequest, messages []model.Message, definitions []model.ToolDefinition) (model.Response, error) {
	modelName := turn.Model
	if modelName == "" {
		modelName = r.config.Model
	}
	// Final admission also protects custom projectors and tool-less recovery
	// requests. Nothing may be appended after this complete request check.
	inputTokens := r.config.Context.counter().Messages(messages) + r.config.Context.counter().Tools(definitions)
	if inputTokens+r.config.Context.ReserveTokens+r.config.Context.OutputTokens > r.config.Context.MaxTokens {
		return model.Response{}, &model.Error{Kind: model.ErrorBudgetExhausted, Message: "assembled model request exceeds the context budget"}
	}
	requestID, err := r.nextModelRequestID(ctx, turn)
	if err != nil {
		return model.Response{}, err
	}
	request := model.Request{
		InputTokenEstimate: inputTokens,
		Model:              modelName, Messages: messages, Tools: definitions,
		ReasoningEffort: r.config.ReasoningEffort, Temperature: r.config.Temperature, MaxOutputTokens: r.config.MaxOutputTokens,
		Metadata: map[string]string{"session_id": turn.SessionID, "turn_id": turn.TurnID, "request_id": requestID},
	}

	var lastErr error
	for attempt := 0; attempt <= r.config.MaxModelRetries; attempt++ {
		metadata, _ := json.Marshal(map[string]any{"request_id": requestID, "attempt": attempt + 1, "model": request.Model})
		if _, err := r.record(ctx, transcript.Event{ID: fmt.Sprintf("model-request:%s:attempt:%d", requestID, attempt+1), SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelRequestStarted, Status: "started", Metadata: metadata}); err != nil {
			return model.Response{}, err
		}
		response, err := r.generateAttempt(ctx, turn, request, attempt+1)
		if err == nil {
			completed, _ := json.Marshal(map[string]any{"request_id": requestID, "attempt": attempt + 1, "model": request.Model, "finish_reason": response.FinishReason, "usage": response.Usage})
			if _, recordErr := r.record(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelCompleted, Metadata: completed, Trace: responseTrace(response)}); recordErr != nil {
				return model.Response{}, recordErr
			}
			return response, nil
		}
		lastErr = err
		var typed *model.Error
		failed, _ := json.Marshal(map[string]any{"request_id": requestID, "attempt": attempt + 1, "model": request.Model, "kind": model.ErrorKindOf(err), "retryable": errors.As(err, &typed) && typed.Retryable})
		failedCtx, failedCancel := r.cleanupContext(ctx)
		_, recordErr := r.record(failedCtx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelFailed, Error: err.Error(), Metadata: failed, Trace: responseTrace(response)})
		failedCancel()
		if recordErr != nil {
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

func (r *Runtime) nextModelRequestID(ctx context.Context, turn TurnRequest) (string, error) {
	r.eventMu.RLock()
	if cached := r.eventCache[turn.SessionID]; cached != nil {
		next := len(cached.requests[turn.TurnID]) + 1
		r.eventMu.RUnlock()
		return fmt.Sprintf("%s:model:%d", turn.TurnID, next), nil
	}
	r.eventMu.RUnlock()
	events, err := r.turnEvents(ctx, turn.SessionID)
	if err != nil {
		return "", err
	}
	seen := make(map[string]bool)
	for _, event := range events {
		if event.TurnID != turn.TurnID || event.Type != transcript.ModelRequestStarted {
			continue
		}
		var state modelRequestState
		if json.Unmarshal(event.Metadata, &state) == nil && state.RequestID != "" {
			seen[state.RequestID] = true
			continue
		}
		// Preserve forward progress for legacy events without request metadata.
		seen[event.ID] = true
	}
	return fmt.Sprintf("%s:model:%d", turn.TurnID, len(seen)+1), nil
}

func (r *Runtime) generateAttempt(ctx context.Context, turn TurnRequest, request model.Request, attempt int) (response model.Response, err error) {
	parent := trace.SpanContextFromContext(ctx)
	modelAttributes := langfuseObservationAttributes(turn.Scope, "generation")
	modelAttributes = append(modelAttributes,
		attribute.String("gen_ai.request.model", request.Model),
		attribute.Int("gen_ai.request.max_tokens", request.MaxOutputTokens),
		attribute.String("agent.model.request_id", request.Metadata["request_id"]),
		attribute.Int("agent.model.attempt", attempt),
	)
	ctx, span := runtimeTracer.Start(ctx, "model.generate", trace.WithAttributes(modelAttributes...))
	stats, _ := ctx.Value(turnTraceKey{}).(*turnTrace)
	attempts, retries, fallbacks := 0, 0, 0
	observed := false
	actualModel, actualProvider := request.Model, ""
	if r.deps.TraceContent != nil && span.IsRecording() {
		input := request
		input.Messages = traceMessages(request.Messages)
		r.traceContent(span, "input", input)
	}
	ctx = model.WithAttemptObserver(ctx, func(attempt model.Attempt) {
		observed = true
		switch attempt.Outcome {
		case "requested":
			attempts++
			actualModel, actualProvider = attempt.Model, attempt.Provider
		case "retrying":
			retries++
		case "fallback":
			fallbacks++
		case "completed":
			actualModel, actualProvider = attempt.Model, attempt.Provider
		}

		span.AddEvent("model.attempt", trace.WithAttributes(
			attribute.String("gen_ai.provider.name", attempt.Provider), attribute.String("gen_ai.request.model", attempt.Model),
			attribute.String("agent.model.outcome", attempt.Outcome), attribute.Int("agent.model.attempt", attempt.Number),
			attribute.Bool("agent.model.fallback", attempt.Fallback), attribute.String("error.type", string(model.ErrorKindOf(attempt.Error))),
		))
		metadata, _ := json.Marshal(map[string]any{"request_id": request.Metadata["request_id"], "attempt": attempt.Number, "provider": attempt.Provider, "model": attempt.Model, "fallback": attempt.Fallback, "outcome": attempt.Outcome, "remaining_ms": attempt.Remaining.Milliseconds(), "kind": model.ErrorKindOf(attempt.Error)})
		event := transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelAttempted, Status: attempt.Outcome, Metadata: metadata}
		if attempt.Error != nil {
			event.Error = attempt.Error.Error()
		}
		attemptCtx, cancel := r.cleanupContext(ctx)
		defer cancel()
		_, _ = r.record(attemptCtx, event)
	})
	defer func() {
		spanContext := span.SpanContext()
		response.TraceID, response.SpanID = spanContext.TraceID().String(), spanContext.SpanID().String()
		if parent.IsValid() {
			response.ParentSpanID = parent.SpanID().String()
		}
		if attempts == 0 && !observed {
			attempts = 1
		} // Clients without a resilience observer make one call.
		recordUsage(span, response.Usage)
		span.SetAttributes(attribute.String("langfuse.observation.model.name", actualModel),
			attribute.String("langfuse.observation.metadata.requested_model", request.Model),
			attribute.String("langfuse.observation.metadata.actual_provider", actualProvider),
			attribute.Int("langfuse.observation.metadata.attempt_count", attempts),
			attribute.Int("langfuse.observation.metadata.retry_count", retries),
			attribute.Int("langfuse.observation.metadata.fallback_count", fallbacks),
			attribute.Bool("langfuse.observation.metadata.recovered", err == nil && (retries > 0 || fallbacks > 0)),
		)
		if stats != nil {
			stats.mu.Lock()
			stats.requests++
			stats.attempts += attempts
			stats.retries += retries
			if attempt > 1 {
				stats.retries++
			}
			stats.fallbacks += fallbacks
			if !response.Usage.Known() {
				stats.missingUsage++
			}
			stats.lastModel, stats.lastProvider = actualModel, actualProvider
			stats.mu.Unlock()
		}
		if r.deps.TraceContent != nil && span.IsRecording() {
			r.traceContent(span, "output", traceMessages([]model.Message{response.Message}))
		}
		span.SetAttributes(
			attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(response.FinishReason)}),
			attribute.Int64("agent.model.cache_read_tokens", response.Usage.CacheReadTokens),
			attribute.Int64("agent.model.cache_created_tokens", response.Usage.CacheCreatedTokens),
		)
		if err != nil {
			recordSpanError(span, err)
			span.SetStatus(codes.Error, "model request failed")
		}
		span.End()
	}()
	started := time.Now()
	var firstToken atomic.Bool
	return r.deps.Model.Generate(ctx, request, func(event model.StreamEvent) error {
		if (event.Type == model.StreamFirstToken || (event.Type == model.StreamTextDelta && event.Text != "")) && firstToken.CompareAndSwap(false, true) {
			now := time.Now()
			span.SetAttributes(attribute.Int64("agent.model.first_token_ms", now.Sub(started).Milliseconds()), attribute.String("langfuse.observation.completion_start_time", now.UTC().Format(time.RFC3339Nano)))
		}
		// Stream deltas are presentation events, not canonical durable facts.
		// Persisting each delta makes PostgreSQL/JSONL cost proportional to token
		// count. The final assistant message and model lifecycle remain durable.
		r.emit(ctx, transcript.Event{SessionID: turn.SessionID, TurnID: turn.TurnID, Type: transcript.ModelStreamed, Model: &event})
		return ctx.Err()
	})
}

func responseTrace(response model.Response) *transcript.TraceContext {
	if response.TraceID == "" || response.SpanID == "" {
		return nil
	}
	return &transcript.TraceContext{TraceID: response.TraceID, SpanID: response.SpanID, ParentSpanID: response.ParentSpanID}
}
