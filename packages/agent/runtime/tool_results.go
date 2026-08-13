package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type ToolResultConfig struct {
	MaxInlineBytes int
	MaxBatchBytes  int
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
	if budget <= 0 {
		budget = 64 << 10
	}
	result.Content = truncateContent(result.Content, budget)
	result.Content = append(result.Content, model.Content{Type: model.ContentText, Text: "[tool output truncated]"})
	return result
}

func limitToolResultBatch(ctx context.Context, prepared []preparedCall, config ToolResultConfig, store ArtifactStore) {
	if config.MaxBatchBytes <= 0 || len(prepared) < 2 {
		return
	}
	total := 0
	for index := range prepared {
		if prepared[index].result == nil {
			continue
		}
		data, err := json.Marshal(prepared[index].result.Content)
		if err == nil {
			total += len(data)
		}
	}
	if total <= config.MaxBatchBytes {
		return
	}
	perResult := config.MaxBatchBytes / len(prepared)
	for index := range prepared {
		entry := &prepared[index]
		if entry.result != nil {
			limited := limitToolResult(ctx, *entry.result, entry.call, ToolResultConfig{MaxInlineBytes: perResult}, store)
			entry.result = &limited
		}
	}
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
				block.Text = truncateUTF8(block.Text, remaining)
			}
			remaining -= len(block.Text)
			out = append(out, block)
		case model.ContentJSON:
			if len(block.JSON) <= remaining {
				remaining -= len(block.JSON)
				block.JSON = append([]byte(nil), block.JSON...)
				out = append(out, block)
			} else {
				text := truncateUTF8(string(block.JSON), remaining)
				remaining -= len(text)
				out = append(out, model.Content{Type: model.ContentText, Text: text})
			}
		}
	}
	return out
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
