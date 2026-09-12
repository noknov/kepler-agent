package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// forceCompactAfterContextLimit shrinks durable history when a provider rejects a
// request for exceeding its context window despite local budgeting.
func (r *Runtime) forceCompactAfterContextLimit(ctx context.Context, request TurnRequest, system model.Message) error {
	if r.deps.Compactor == nil {
		return errors.New("compactor unavailable")
	}
	events, err := r.turnEvents(ctx, request.SessionID)
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
		toCompact, coversThrough = messagesBeforeCurrentTurn(events, request.TurnID)
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

func messagesBeforeCurrentTurn(events []transcript.Event, turnID string) ([]model.Message, uint64) {
	var protected uint64
	for _, event := range events {
		if turnID != "" && event.TurnID == turnID && event.Type == transcript.UserInput {
			protected = event.Sequence
			break
		}
	}
	if protected == 0 {
		for index := len(events) - 1; index >= 0; index-- {
			if events[index].Type == transcript.UserInput || events[index].Type == transcript.SteeringInput {
				protected = events[index].Sequence
				break
			}
		}
	}
	var messages []model.Message
	var coversThrough uint64
	for _, event := range events {
		if protected > 0 && event.Sequence >= protected {
			break
		}
		if message, ok := eventMessage(event); ok {
			messages = append(messages, message)
			coversThrough = event.Sequence
		}
	}
	return messages, coversThrough
}
