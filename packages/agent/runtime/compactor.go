package runtime

import (
	"context"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

type ModelCompactor struct {
	Client          model.Client
	Model           string
	MaxOutputTokens int
}

func (c ModelCompactor) Compact(ctx context.Context, messages []model.Message, _ int) (model.Message, error) {
	if c.Client == nil {
		return model.Message{}, fmt.Errorf("compactor model client is required")
	}
	instructions := model.TextMessage(model.RoleSystem, "Summarize the conversation state for another agent. Preserve user intent, decisions, evidence, file paths, tool results, unresolved questions, and constraints. Do not invent facts.")
	requestMessages := make([]model.Message, 0, len(messages)+1)
	requestMessages = append(requestMessages, instructions)
	requestMessages = append(requestMessages, messages...)
	maxTokens := c.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 4_096
	}
	response, err := c.Client.Generate(ctx, model.Request{Model: c.Model, Messages: requestMessages, MaxOutputTokens: maxTokens}, nil)
	if err != nil {
		return model.Message{}, err
	}
	if response.Message.Text() == "" {
		return model.Message{}, fmt.Errorf("compactor returned an empty summary")
	}
	return model.TextMessage(model.RoleSystem, "Compacted conversation context:\n"+response.Message.Text()), nil
}
