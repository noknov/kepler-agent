package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func (r *Runtime) projectContext(ctx context.Context, request TurnRequest, system model.Message) (Projection, error) {
	for round := 0; round < 3; round++ {
		projection, err := r.projectContextOnce(ctx, request, system)
		if err != nil {
			return Projection{}, err
		}
		if len(projection.Dropped) == 0 || r.deps.Compactor == nil {
			return projection, nil
		}
	}
	return Projection{}, &model.Error{Kind: model.ErrorBudgetExhausted, Message: "compaction could not fit a complete continuation checkpoint"}
}

func (r *Runtime) projectContextOnce(ctx context.Context, request TurnRequest, system model.Message) (Projection, error) {
	events, err := r.contextEvents(ctx, request.SessionID)
	if err != nil {
		return Projection{}, err
	}
	projectRequest := r.projectionRequest(events, request, system, r.deps.Tools.ActiveDefinitions(request.SessionID))
	if r.deps.Compactor != nil {
		available, err := r.config.Context.inputBudget(projectRequest.Tools)
		if err != nil {
			return Projection{}, err
		}
		projectRequest.TargetTokens = available * r.config.Context.Policy.WithDefaults().SoftLimitPercent / 100
	}
	projection, err := r.deps.Projector.Project(ctx, projectRequest)
	if err != nil && projectRequest.TargetTokens > 0 {
		projectRequest.TargetTokens = 0
		projection, err = r.deps.Projector.Project(ctx, projectRequest)
	}
	if err != nil {
		return Projection{}, err
	}
	needsReader := projection.MaskedEvidence
	for _, message := range projection.Dropped {
		for _, block := range message.Content {
			needsReader = needsReader || block.ToolResult != nil
		}
	}
	if needsReader {
		if _, active := r.deps.Tools.GetActive(request.SessionID, "context_read"); !active {
			if err := r.deps.Tools.Activate(request.SessionID, "context_read"); err != nil {
				return Projection{}, err
			}
			projectRequest.Tools = r.deps.Tools.ActiveDefinitions(request.SessionID)
			projectRequest.TargetTokens = 0
			projection, err = r.deps.Projector.Project(ctx, projectRequest)
			if err != nil {
				return Projection{}, err
			}
		}
	}
	if len(projection.Dropped) == 0 || r.deps.Compactor == nil {
		return projection, nil
	}
	if err := r.compactContext(ctx, request, projection, false); err != nil {
		// A proactive outage may use the hard budget only when no source history
		// is discarded. It never silently turns a failed summary into truncation.
		if ctx.Err() == nil {
			hard, projectErr := r.deps.Projector.Project(ctx, r.projectionRequest(events, request, system, r.deps.Tools.ActiveDefinitions(request.SessionID)))
			if projectErr == nil && len(hard.Dropped) == 0 {
				return hard, nil
			}
		}
		return Projection{}, err
	}

	events, err = r.contextEvents(ctx, request.SessionID)
	if err != nil {
		return Projection{}, err
	}
	projection, err = r.deps.Projector.Project(ctx, r.projectionRequest(events, request, system, r.deps.Tools.ActiveDefinitions(request.SessionID)))
	if err != nil {
		return Projection{}, err
	}
	return projection, err
}

// compactContext is the sole checkpoint commit path for proactive and rejected
// requests. Its output ceiling comes from the actual remaining input capacity.
func (r *Runtime) compactContext(ctx context.Context, request TurnRequest, projection Projection, forced bool) (err error) {
	ctx, span := runtimeTracer.Start(ctx, "context.compact", trace.WithAttributes(
		attribute.Bool("agent.context.forced", forced),
		attribute.Int("agent.context.retained_tokens", projection.EstimatedTokens),
		attribute.Int("agent.context.compacted_messages", len(projection.Dropped)),
	))
	defer func() {
		if err != nil {
			span.SetAttributes(attribute.String("error.type", string(model.ErrorKindOf(err))))
			span.SetStatus(codes.Error, "checkpoint failed")
		}
		span.End()
	}()
	available, err := r.config.Context.inputBudget(r.deps.Tools.ActiveDefinitions(request.SessionID))
	if err != nil {
		return err
	}
	counter := r.config.Context.counter()
	envelope := model.TextMessage(model.RoleUser, "<untrusted_transcript_summary>\n\n</untrusted_transcript_summary>")
	overhead := counter.Messages([]model.Message{envelope, untrustedTranscriptSummaryBoundary})
	target := min(defaultCompactionOutputTokens, available-projection.EstimatedTokens-overhead)
	if target <= 0 {
		return &model.Error{Kind: model.ErrorBudgetExhausted, Message: "no input capacity remains for a continuation checkpoint"}
	}
	started := time.Now()
	summary, err := r.deps.Compactor.Compact(ctx, compactionEvidence(projection), target)
	if err != nil {
		return err
	}
	summaryTokens := counter.Messages([]model.Message{summary})
	span.SetAttributes(attribute.Int("agent.context.summary_tokens", summaryTokens), attribute.Int("agent.context.summary_budget", target))
	if len(summary.Content) == 0 || summaryTokens+projection.EstimatedTokens+counter.Messages([]model.Message{untrustedTranscriptSummaryBoundary}) > available {
		return &model.Error{Kind: model.ErrorBudgetExhausted, Message: "compactor returned an empty or oversized continuation checkpoint"}
	}
	metadata, err := json.Marshal(map[string]any{
		"covers_through": projection.CoversThrough, "forced": forced,
		"context_policy":      r.config.Context.Policy.WithDefaults(),
		"native_continuation": hasNativeContinuation(summary),
		"retained_tokens":     projection.EstimatedTokens, "summary_tokens": summaryTokens,
		"summary_budget": target, "duration_ms": time.Since(started).Milliseconds(),
	})
	if err != nil {
		return err
	}
	_, err = r.record(ctx, transcript.Event{
		SessionID: request.SessionID, TurnID: request.TurnID,
		Type: transcript.CompactionCreated, Message: &summary, Metadata: metadata,
	})
	return err
}

func compactionEvidence(projection Projection) []model.Message {
	messages := append([]model.Message(nil), projection.Dropped...)
	for i, message := range messages {
		if i >= len(projection.DroppedSequences) {
			continue
		}
		message.Content = append([]model.Content(nil), message.Content...)
		for j, block := range message.Content {
			if block.ToolResult == nil {
				continue
			}
			result := *block.ToolResult
			result.Content = append([]model.Content(nil), result.Content...)
			result.Content = append(result.Content, model.Content{Type: model.ContentText, Text: fmt.Sprintf("Original evidence reference: context_read(sequence=%d, block=0, offset=0). Blocks are numbered 0 through %d. Retain relevant references in the checkpoint.", projection.DroppedSequences[i], len(result.Content)-1)})
			message.Content[j].ToolResult = &result
		}
		messages[i] = message
	}
	return messages
}
