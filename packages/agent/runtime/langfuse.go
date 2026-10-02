package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// langfuseTraceAttributes are copied onto every runtime span. Langfuse v4
// queries observations directly, so putting user/session/surface only on the
// root span would make child model and tool spans difficult to filter.
// Content disclosure is injected by profiles, separate from prompt policy.
func langfuseTraceAttributes(scope tool.Scope) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String("agent.session.id", scope.SessionID),
		attribute.String("agent.turn.id", scope.TurnID),
		attribute.String("langfuse.session.id", scope.SessionID),
		attribute.String("langfuse.trace.name", "kepler-agent.turn"),
		attribute.String("langfuse.observation.metadata.turn_id", scope.TurnID),
	}
	if scope.UserID != "" {
		attributes = append(attributes, attribute.String("langfuse.user.id", scope.UserID))
	}
	if surface := strings.TrimSpace(scope.Values["surface"]); surface != "" {
		attributes = append(attributes,
			attribute.String("agent.surface", surface),
			attribute.String("langfuse.trace.metadata.surface", surface),
			attribute.String("langfuse.observation.metadata.surface", surface),
		)
	}
	if hash := scope.Values["prompt_hash"]; hash != "" {
		attributes = append(attributes, attribute.String("langfuse.version", hash), attribute.String("langfuse.observation.metadata.prompt_hash", hash))
	}
	return attributes
}

// Provider errors may include response bodies, URLs, or echoed request data.
// Keep full diagnostics in the local transcript, not in an external trace.
func recordSpanError(span trace.Span, err error) {
	kind := string(model.ErrorKindOf(err))
	if errors.Is(err, context.Canceled) {
		kind = "canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		kind = "deadline_exceeded"
	}
	span.SetAttributes(attribute.String("error.type", kind), attribute.String("langfuse.observation.metadata.error_kind", kind))
	span.RecordError(errors.New(kind))
}

func (r *Runtime) traceContent(span trace.Span, field string, value any) {
	if r.deps.TraceContent != nil && span.IsRecording() {
		r.deps.TraceContent(span, field, value)
	}
}

// Omit image bytes/URLs and internal reasoning from external debugging data.
func traceMessages(messages []model.Message) []model.Message {
	result := make([]model.Message, 0, len(messages))
	for _, message := range messages {
		copy := message
		copy.Content = make([]model.Content, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case model.ContentReasoning:
				continue
			case model.ContentImage:
				block = model.Content{Type: model.ContentText, Text: "[image omitted]"}
			case model.ContentArtifact:
				if block.Artifact != nil {
					artifact := *block.Artifact
					artifact.URI = ""
					block.Artifact = &artifact
				}
			}
			copy.Content = append(copy.Content, block)
		}
		result = append(result, copy)
	}
	return result
}

type turnTraceKey struct{}
type turnTrace struct {
	mu                                                                 sync.Mutex
	requests, attempts, retries, fallbacks, missingUsage, toolFailures int
	lastModel, lastProvider                                            string
}

func (r *Runtime) finishTrace(span trace.Span, stats *turnTrace, result TurnResult, err error) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	status := "completed"
	switch {
	case result.Termination == TerminationCanceled || errors.Is(err, context.Canceled):
		status = "canceled"
		span.SetStatus(codes.Unset, "")
		span.SetAttributes(attribute.String("langfuse.observation.level", "WARNING"))
	case result.Termination == TerminationMaxSteps || result.Termination == TerminationOutputLimit || result.Termination == TerminationBudgetExhausted:
		status = "limited"
	case err != nil:
		status = "failed"
		recordSpanError(span, err)
		span.SetStatus(codes.Error, "agent turn failed")
	case result.Termination == TerminationPendingApproval || result.Termination == TerminationPendingInput:
		status = "waiting"
	}
	span.SetAttributes(
		attribute.String("langfuse.observation.metadata.run_status", status),
		attribute.String("langfuse.observation.metadata.termination", string(result.Termination)),
		attribute.Int("langfuse.observation.metadata.steps", result.Steps),
		attribute.Int("langfuse.observation.metadata.model_requests", stats.requests),
		attribute.Int("langfuse.observation.metadata.model_attempts", stats.attempts),
		attribute.Int("langfuse.observation.metadata.model_retries", stats.retries),
		attribute.Int("langfuse.observation.metadata.model_fallbacks", stats.fallbacks),
		attribute.Bool("langfuse.observation.metadata.recovered", err == nil && (stats.retries > 0 || stats.fallbacks > 0)),
		attribute.Int("langfuse.observation.metadata.usage_missing_requests", stats.missingUsage),
		attribute.Int("langfuse.observation.metadata.tool_failures", stats.toolFailures),
		attribute.String("langfuse.observation.metadata.final_model", stats.lastModel),
		attribute.String("langfuse.observation.metadata.final_provider", stats.lastProvider),
	)
	if r.deps.TraceContent != nil && span.IsRecording() {
		r.traceContent(span, "output", traceMessages([]model.Message{result.Message}))
	}
}

func recordUsage(span trace.Span, usage model.Usage) {
	span.SetAttributes(attribute.Bool("langfuse.observation.metadata.usage_reported", usage.Known()))
	if !usage.Known() {
		return
	}
	input, output := max(0, usage.InputTokens), max(0, usage.OutputTokens)
	read, created := max(0, usage.CacheReadTokens), max(0, usage.CacheCreatedTokens)
	if usage.CacheTokensIncludedInInput {
		read = min(read, input)
		created = min(created, input-read)
		input -= read + created
	}
	reasoning := min(max(0, usage.ReasoningTokens), output)
	// Langfuse usage buckets are exclusive; never count cached/reasoning
	// tokens again on top of their inclusive provider totals.
	buckets := map[string]int64{"input": input, "output": output - reasoning, "input_cached_tokens": read, "input_cache_creation_tokens": created, "output_reasoning_tokens": reasoning}
	encoded, _ := json.Marshal(buckets)
	span.SetAttributes(attribute.String("langfuse.observation.usage_details", string(encoded)),
		attribute.Int64("langfuse.observation.metadata.cache_read_tokens", read),
		attribute.Int64("langfuse.observation.metadata.cache_created_tokens", created))
	if total := input + read + created; total > 0 {
		span.SetAttributes(attribute.Float64("langfuse.observation.metadata.cache_hit_ratio", float64(read)/float64(total)))
	}
}

func langfuseObservationAttributes(scope tool.Scope, observationType string) []attribute.KeyValue {
	attributes := langfuseTraceAttributes(scope)
	return append(attributes, attribute.String("langfuse.observation.type", observationType))
}
