// Package legacy adapts the established model and tool implementations to the
// provider-neutral v2 harness. It is deliberately one-way: product surfaces
// depend on v2 contracts while the proven integrations can be migrated
// independently.
package legacy

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type Model struct{ Client llm.Client }

func (m Model) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	req := llm.Request{Model: request.Model, MaxTokens: request.MaxOutputTokens, Thinking: request.ReasoningEffort}
	for _, message := range request.Messages {
		req.Messages = append(req.Messages, toLegacyMessage(message))
	}
	for _, definition := range request.Tools {
		var parameters map[string]any
		_ = json.Unmarshal(definition.InputSchema, &parameters)
		req.Tools = append(req.Tools, llm.ToolSpec{Type: "function", Function: llm.ToolSpecFunction{Name: definition.Name, Description: definition.Description, Parameters: parameters}})
	}
	var response llm.Response
	var err error
	var sinkMu sync.Mutex
	var sinkErr error
	publish := func(event model.StreamEvent) {
		if sink == nil {
			return
		}
		sinkMu.Lock()
		defer sinkMu.Unlock()
		if sinkErr == nil {
			sinkErr = sink(event)
		}
	}
	if stream, ok := m.Client.(llm.StreamClient); ok {
		response, err = stream.ChatStream(ctx, req, llm.StreamHandler{
			OnText: func(delta string) {
				if delta != "" {
					publish(model.StreamEvent{Type: model.StreamTextDelta, Text: delta})
				}
			},
			OnToolCallComplete: func(call llm.ToolCall) {
				canonical := toToolCall(call)
				publish(model.StreamEvent{Type: model.StreamToolCallDone, ToolCall: &canonical})
			},
			OnUsage: func(usage llm.Usage) {
				canonical := toUsage(usage)
				publish(model.StreamEvent{Type: model.StreamUsage, Usage: &canonical})
			},
		})
	} else {
		response, err = m.Client.Chat(ctx, req)
	}
	if err != nil {
		return model.Response{}, err
	}
	if sinkErr != nil {
		return model.Response{}, sinkErr
	}
	return model.Response{Message: fromLegacyMessage(response.Message), FinishReason: finishReason(response.FinishReason), Usage: toUsage(response.Usage), RawMetadata: response.Raw}, nil
}

func toLegacyMessage(message model.Message) llm.Message {
	out := llm.Message{Role: string(message.Role)}
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			out.Content += block.Text
		case model.ContentReasoning:
			out.ReasoningContent += block.Text
		case model.ContentToolCall:
			if block.ToolCall != nil {
				out.ToolCalls = append(out.ToolCalls, llm.ToolCall{ID: block.ToolCall.ID, Type: "function", Function: llm.ToolFunction{Name: block.ToolCall.Name, Arguments: string(block.ToolCall.Arguments)}})
			}
		case model.ContentToolResult:
			if block.ToolResult != nil {
				out.Role = "tool"
				out.ToolCallID = block.ToolResult.CallID
				out.Name = block.ToolResult.Name
				for _, content := range block.ToolResult.Content {
					if content.Type == model.ContentText {
						out.Content += content.Text
					}
				}
			}
		}
	}
	return out
}

func fromLegacyMessage(message llm.Message) model.Message {
	out := model.Message{Role: model.Role(message.Role)}
	if message.ReasoningContent != "" {
		out.Content = append(out.Content, model.Content{Type: model.ContentReasoning, Text: message.ReasoningContent})
	}
	if message.Content != "" {
		out.Content = append(out.Content, model.Content{Type: model.ContentText, Text: message.Content})
	}
	for _, call := range message.ToolCalls {
		canonical := toToolCall(call)
		out.Content = append(out.Content, model.Content{Type: model.ContentToolCall, ToolCall: &canonical})
	}
	return out
}

func toToolCall(call llm.ToolCall) model.ToolCall {
	arguments := json.RawMessage(call.Function.Arguments)
	if !json.Valid(arguments) {
		arguments = json.RawMessage(`{}`)
	}
	return model.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments}
}

func toUsage(usage llm.Usage) model.Usage {
	return model.Usage{InputTokens: int64(usage.PromptTokens), OutputTokens: int64(usage.CompletionTokens), CacheReadTokens: int64(usage.CacheReadInputTokens), CacheCreatedTokens: int64(usage.CacheCreationInputTokens)}
}

func finishReason(reason string) model.FinishReason {
	switch strings.ToLower(reason) {
	case "tool_calls", "tool_use":
		return model.FinishToolCalls
	case "length", "max_tokens":
		return model.FinishLength
	case "content_filter":
		return model.FinishContent
	default:
		return model.FinishStop
	}
}
