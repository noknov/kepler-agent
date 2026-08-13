package runtime

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

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
	group    string
	message  model.Message
}

type projectedGroup struct {
	entries []projectedMessage
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
		base.group = "compaction"
		entries = append(entries, *base)
	}
	fallbackTurn := 0
	for _, event := range events {
		if event.Sequence <= baseSequence {
			continue
		}
		message, ok := eventMessage(event)
		if ok {
			group := event.TurnID
			if group == "" {
				if event.Type == transcript.UserInput || event.Type == transcript.SteeringInput || fallbackTurn == 0 {
					fallbackTurn++
				}
				group = "fallback:" + strconv.Itoa(fallbackTurn)
			}
			entries = append(entries, projectedMessage{sequence: event.Sequence, group: group, message: message})
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
	groups := groupProjectedMessages(entries)
	if tokens <= limit || len(groups) <= 1 {
		return Projection{Messages: messages, EstimatedTokens: tokens}, nil
	}
	keepGroup := 0
	keptTokens := EstimateTokens([]model.Message{system})
	for index := len(groups) - 1; index >= 0; index-- {
		groupMessages := make([]model.Message, 0, len(groups[index].entries))
		for _, entry := range groups[index].entries {
			groupMessages = append(groupMessages, entry.message)
		}
		groupTokens := EstimateTokens(groupMessages)
		if keptTokens+groupTokens > limit && index < len(groups)-1 {
			keepGroup = index + 1
			break
		}
		keptTokens += groupTokens
	}
	if keepGroup == 0 {
		return Projection{Messages: messages, EstimatedTokens: tokens}, nil
	}
	keepFrom := 0
	for index := 0; index < keepGroup; index++ {
		keepFrom += len(groups[index].entries)
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

func groupProjectedMessages(entries []projectedMessage) []projectedGroup {
	groups := make([]projectedGroup, 0, len(entries))
	for _, entry := range entries {
		if len(groups) == 0 || groups[len(groups)-1].entries[0].group != entry.group {
			groups = append(groups, projectedGroup{})
		}
		last := len(groups) - 1
		groups[last].entries = append(groups[last].entries, entry)
	}
	return groups
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
	tokens := 0
	for _, message := range messages {
		tokens += estimateText(string(message.Role)) + 4
		for _, block := range message.Content {
			tokens += estimateText(block.Text) + estimateText(string(block.JSON)) + 4
			if block.Type == model.ContentImage {
				// Image tokenization depends on provider and dimensions. Reserve a
				// conservative fixed cost and account for inline data URLs so large
				// base64 payloads cannot bypass compaction.
				tokens += 1024 + estimateText(block.ImageURL)
			}
			if block.Artifact != nil {
				tokens += estimateText(block.Artifact.URI) + estimateText(block.Artifact.Name)
			}
			if block.ToolCall != nil {
				tokens += estimateText(block.ToolCall.Name) + estimateText(string(block.ToolCall.Arguments)) + 8
			}
			if block.ToolResult != nil {
				tokens += estimateText(block.ToolResult.Name) + 8
				for _, nested := range block.ToolResult.Content {
					tokens += estimateText(nested.Text) + estimateText(string(nested.JSON))
					if nested.Type == model.ContentImage {
						tokens += 1024 + estimateText(nested.ImageURL)
					}
				}
			}
		}
	}
	return tokens
}

func estimateText(value string) int {
	if value == "" {
		return 0
	}
	if strings.HasPrefix(value, "data:") {
		return (len(value) + 3) / 4
	}
	ascii, nonASCII := 0, 0
	for _, r := range value {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	return (ascii+3)/4 + nonASCII
}
