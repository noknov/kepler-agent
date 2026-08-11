package runtime

import (
	"context"
	"encoding/json"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

type ContextConfig struct {
	MaxTokens     int
	ReserveTokens int
}

type Projection struct {
	Messages        []model.Message
	EstimatedTokens int
	Dropped         []model.Message
	CoversThrough   uint64
}

type Projector interface {
	Project(ctx context.Context, events []transcript.Event, system model.Message) (Projection, error)
}

type Compactor interface {
	Compact(ctx context.Context, messages []model.Message, targetTokens int) (model.Message, error)
}

type BoundedProjector struct {
	config ContextConfig
}

func NewBoundedProjector(config ContextConfig) BoundedProjector {
	return BoundedProjector{config: config}
}

type projectedMessage struct {
	sequence uint64
	message  model.Message
}

func (p BoundedProjector) Project(_ context.Context, events []transcript.Event, system model.Message) (Projection, error) {
	limit := p.config.MaxTokens - p.config.ReserveTokens
	if limit <= 0 {
		limit = p.config.MaxTokens
	}
	var base *projectedMessage
	var baseSequence uint64
	for _, event := range events {
		if event.Type == transcript.CompactionCreated && event.Message != nil {
			base = &projectedMessage{sequence: event.Sequence, message: *event.Message}
			baseSequence = compactionCoverage(event)
		}
	}
	entries := make([]projectedMessage, 0, len(events)+1)
	if base != nil {
		entries = append(entries, *base)
	}
	for _, event := range events {
		if event.Sequence <= baseSequence {
			continue
		}
		message, ok := eventMessage(event)
		if ok {
			entries = append(entries, projectedMessage{sequence: event.Sequence, message: message})
		}
	}
	messages := make([]model.Message, 0, len(entries)+1)
	if len(system.Content) > 0 {
		messages = append(messages, system)
	}
	for _, entry := range entries {
		messages = append(messages, entry.message)
	}
	tokens := EstimateTokens(messages)
	if tokens <= limit || len(entries) <= 2 {
		return Projection{Messages: messages, EstimatedTokens: tokens}, nil
	}
	keepFrom := 0
	keptTokens := EstimateTokens([]model.Message{system})
	for index := len(entries) - 1; index >= 0; index-- {
		entryTokens := EstimateTokens([]model.Message{entries[index].message})
		if keptTokens+entryTokens > limit && index < len(entries)-2 {
			keepFrom = index + 1
			break
		}
		keptTokens += entryTokens
	}
	if keepFrom == 0 {
		return Projection{Messages: messages, EstimatedTokens: tokens}, nil
	}
	dropped := make([]model.Message, 0, keepFrom)
	for _, entry := range entries[:keepFrom] {
		dropped = append(dropped, entry.message)
	}
	kept := make([]model.Message, 0, len(entries)-keepFrom+1)
	if len(system.Content) > 0 {
		kept = append(kept, system)
	}
	for _, entry := range entries[keepFrom:] {
		kept = append(kept, entry.message)
	}
	return Projection{
		Messages: kept, EstimatedTokens: EstimateTokens(kept), Dropped: dropped,
		CoversThrough: entries[keepFrom-1].sequence,
	}, nil
}

func eventMessage(event transcript.Event) (model.Message, bool) {
	switch event.Type {
	case transcript.UserInput, transcript.SteeringInput, transcript.AssistantMessage:
		if event.Message != nil {
			return *event.Message, true
		}
	case transcript.ToolCallCompleted, transcript.ToolCallFailed:
		if event.ToolCall != nil && event.ToolResult != nil {
			result := model.ToolResult{
				CallID: event.ToolCall.ID, Name: event.ToolCall.Name,
				Content: event.ToolResult.Content, IsError: event.ToolResult.IsError,
			}
			return model.Message{Role: model.RoleTool, Content: []model.Content{{Type: model.ContentToolResult, ToolResult: &result}}}, true
		}
	}
	return model.Message{}, false
}

func compactionCoverage(event transcript.Event) uint64 {
	var metadata struct {
		CoversThrough uint64 `json:"covers_through"`
	}
	if json.Unmarshal(event.Metadata, &metadata) == nil {
		return metadata.CoversThrough
	}
	return 0
}

func EstimateTokens(messages []model.Message) int {
	bytes := 0
	for _, message := range messages {
		bytes += len(message.Role) + 8
		for _, block := range message.Content {
			bytes += len(block.Text) + len(block.JSON) + 8
			if block.ToolCall != nil {
				bytes += len(block.ToolCall.Name) + len(block.ToolCall.Arguments) + 16
			}
			if block.ToolResult != nil {
				bytes += len(block.ToolResult.Name) + 16
				for _, nested := range block.ToolResult.Content {
					bytes += len(nested.Text) + len(nested.JSON)
				}
			}
		}
	}
	return (bytes + 3) / 4
}
