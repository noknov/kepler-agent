package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

// forceCompactAfterContextLimit shrinks durable history when a provider rejects a
// request for exceeding its context window despite local budgeting.
func (r *Runtime) forceCompactAfterContextLimit(ctx context.Context, request TurnRequest, system model.Message) error {
	if r.deps.Compactor == nil {
		return errors.New("compactor unavailable")
	}
	events, err := r.deps.Transcript.Load(ctx, request.SessionID, 0)
	if err != nil {
		return err
	}
	tight := r.config.Context
	if tight.MaxTokens > 8192 {
		tight.MaxTokens = tight.MaxTokens / 2
	}
	projection, err := NewBoundedProjector(tight).Project(ctx, events, system)
	if err != nil {
		return err
	}
	toCompact := append([]model.Message(nil), projection.Dropped...)
	coversThrough := projection.CoversThrough
	if len(toCompact) == 0 {
		full, projErr := NewBoundedProjector(r.config.Context).Project(ctx, events, model.Message{})
		if projErr != nil {
			return projErr
		}
		start := 0
		if len(system.Content) > 0 && len(full.Messages) > 0 && full.Messages[0].Role == model.RoleSystem {
			start = 1
		}
		if len(full.Messages)-start <= 2 {
			return fmt.Errorf("context still too large and nothing removable")
		}
		toCompact = append([]model.Message(nil), full.Messages[start:len(full.Messages)-2]...)
		coversThrough = sequenceBeforeTail(events, 2)
	}
	if len(toCompact) == 0 {
		return fmt.Errorf("context still too large and nothing removable")
	}
	targetTokens := r.config.Context.MaxTokens / 8
	if targetTokens < 512 {
		targetTokens = 512
	}
	summary, err := r.deps.Compactor.Compact(ctx, toCompact, targetTokens)
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"covers_through": coversThrough, "forced": true})
	_, err = r.record(ctx, transcript.Event{
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Type:      transcript.CompactionCreated,
		Message:   &summary,
		Metadata:  metadata,
	})
	return err
}

func sequenceBeforeTail(events []transcript.Event, tail int) uint64 {
	if len(events) <= tail {
		return 0
	}
	return events[len(events)-tail-1].Sequence
}
