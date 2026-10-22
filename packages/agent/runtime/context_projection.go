package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
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
		available := r.config.Context.MaxTokens - r.config.Context.ReserveTokens - r.config.Context.OutputTokens - r.config.Context.counter().Tools(projectRequest.Tools)
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
	targetTokens := 4_096
	if smallContextTarget := r.config.Context.MaxTokens / 8; smallContextTarget > 0 && smallContextTarget < targetTokens {
		targetTokens = smallContextTarget
	}
	if targetTokens < 512 {
		targetTokens = 512
	}
	evidence := compactionEvidence(projection)
	summary, err := r.deps.Compactor.Compact(ctx, evidence, targetTokens)
	if err != nil {
		// A proactive compaction outage must not terminate work that still fits
		// safely in the hard input budget. Never silently discard history here.
		if ctx.Err() == nil {
			hard, projectErr := r.deps.Projector.Project(ctx, r.projectionRequest(events, request, system, r.deps.Tools.ActiveDefinitions(request.SessionID)))
			if projectErr == nil && len(hard.Dropped) == 0 {
				return hard, nil
			}
		}
		return Projection{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"covers_through": projection.CoversThrough, "context_policy": r.config.Context.Policy.WithDefaults(), "native_continuation": hasNativeContinuation(summary)})
	if _, err = r.record(ctx, transcript.Event{
		SessionID: request.SessionID, TurnID: request.TurnID, Type: transcript.CompactionCreated,
		Message: &summary, Metadata: metadata,
	}); err != nil {
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
