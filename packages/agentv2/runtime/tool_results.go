package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
)

type ToolResultConfig struct {
	MaxInlineBytes int
}

type ArtifactStore interface {
	Put(ctx context.Context, scope tool.Scope, name string, content []byte) (model.Artifact, error)
}

func limitToolResult(ctx context.Context, result tool.Result, call tool.Call, config ToolResultConfig, store ArtifactStore) tool.Result {
	data, err := json.Marshal(result.Content)
	if err != nil || len(data) <= config.MaxInlineBytes {
		return result
	}
	result.Truncated = true
	if store != nil {
		artifact, putErr := store.Put(ctx, call.Scope, fmt.Sprintf("%s-%s.json", call.Name, call.ID), data)
		if putErr == nil {
			result.Spill = &artifact
			result.Content = []model.Content{{
				Type: model.ContentText,
				Text: fmt.Sprintf("Tool output was stored as artifact %s (%d bytes).", artifact.URI, artifact.SizeBytes),
			}, {Type: model.ContentArtifact, Artifact: &artifact}}
			return result
		}
	}
	budget := config.MaxInlineBytes
	if budget < 256 {
		budget = 256
	}
	result.Content = truncateContent(result.Content, budget)
	result.Content = append(result.Content, model.Content{Type: model.ContentText, Text: "[tool output truncated]"})
	return result
}

func truncateContent(content []model.Content, budget int) []model.Content {
	remaining := budget
	out := make([]model.Content, 0, len(content))
	for _, block := range content {
		if remaining <= 0 {
			break
		}
		switch block.Type {
		case model.ContentText, model.ContentReasoning:
			if len(block.Text) > remaining {
				block.Text = block.Text[:remaining]
			}
			remaining -= len(block.Text)
			out = append(out, block)
		case model.ContentJSON:
			if len(block.JSON) > remaining {
				block.JSON = append([]byte(nil), block.JSON[:remaining]...)
			}
			remaining -= len(block.JSON)
			out = append(out, block)
		}
	}
	return out
}
