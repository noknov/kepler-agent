package appserver

import (
	"encoding/json"
	"strconv"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

func itoa(value uint64) string { return strconv.FormatUint(value, 10) }

// ItemKind mirrors Codex item types projected from canonical transcript events.
type ItemKind string

const (
	ItemUserMessage      ItemKind = "user_message"
	ItemAssistantMessage ItemKind = "assistant_message"
	ItemToolCall         ItemKind = "tool_call"
	ItemApproval         ItemKind = "approval"
	ItemCompaction       ItemKind = "compaction"
	ItemLifecycle        ItemKind = "lifecycle"
)

// Item is a Codex-style turn item derived from one transcript event.
type Item struct {
	ID        string          `json:"id"`
	Kind      ItemKind        `json:"kind"`
	SessionID string          `json:"sessionId"`
	TurnID    string          `json:"turnId,omitempty"`
	Sequence  uint64          `json:"sequence"`
	Type      transcript.EventType `json:"eventType"`
	Status    string          `json:"status,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func itemFromEvent(event transcript.Event) Item {
	item := Item{
		ID:        event.ID,
		Kind:      itemKind(event.Type),
		SessionID: event.SessionID,
		TurnID:    event.TurnID,
		Sequence:  event.Sequence,
		Type:      event.Type,
		Status:    event.Status,
	}
	if payload, err := json.Marshal(event); err == nil {
		item.Payload = payload
	}
	if item.ID == "" {
		item.ID = "evt_" + itoa(event.Sequence)
	}
	return item
}

func itemKind(eventType transcript.EventType) ItemKind {
	switch eventType {
	case transcript.UserInput, transcript.SteeringInput:
		return ItemUserMessage
	case transcript.AssistantMessage, transcript.ModelCompleted, transcript.ModelStreamed:
		return ItemAssistantMessage
	case transcript.ToolCallStarted, transcript.ToolCallCompleted, transcript.ToolCallFailed:
		return ItemToolCall
	case transcript.ApprovalRequested, transcript.ApprovalResolved:
		return ItemApproval
	case transcript.CompactionCreated:
		return ItemCompaction
	default:
		return ItemLifecycle
	}
}

func itemsFromEvents(events []transcript.Event) []Item {
	items := make([]Item, 0, len(events))
	for _, event := range events {
		items = append(items, itemFromEvent(event))
	}
	return items
}
