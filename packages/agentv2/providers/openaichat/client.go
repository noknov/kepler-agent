// Package openaichat adapts OpenAI-compatible chat completions to model.Client.
package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/providers/internal/sse"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	Headers map[string]string
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

func (c *Client) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	body := map[string]any{
		"model": request.Model, "messages": encodeMessages(request.Messages), "stream": true,
		"stream_options": map[string]bool{"include_usage": true},
	}
	if request.MaxOutputTokens > 0 {
		body["max_completion_tokens"] = request.MaxOutputTokens
	}
	if request.ReasoningEffort != "" {
		body["reasoning_effort"] = request.ReasoningEffort
	}
	if len(request.Tools) > 0 {
		body["tools"] = encodeTools(request.Tools)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return model.Response{}, err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return model.Response{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for key, value := range c.Headers {
		httpRequest.Header.Set(key, value)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	httpResponse, err := client.Do(httpRequest)
	if err != nil {
		return model.Response{}, classifyTransport(err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return model.Response{}, decodeHTTPError(httpResponse)
	}

	if sink != nil {
		_ = sink(model.StreamEvent{Type: model.StreamStarted})
	}
	var response model.Response
	var text, reasoning strings.Builder
	calls := make(map[int]*model.ToolCall)
	err = sse.Read(httpResponse.Body, func(event sse.Event) error {
		if string(event.Data) == "[DONE]" {
			return nil
		}
		var chunk struct {
			ID      string `json:"id"`
			Choices []struct {
				Delta struct {
					Content   string         `json:"content"`
					Reasoning string         `json:"reasoning_content"`
					ToolCalls []chatToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt        int64 `json:"prompt_tokens"`
				Completion    int64 `json:"completion_tokens"`
				Cached        int64 `json:"cached_tokens"`
				PromptDetails struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(event.Data, &chunk); err != nil {
			return err
		}
		if chunk.ID != "" {
			response.ID = chunk.ID
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptDetails.Cached > chunk.Usage.Cached {
				chunk.Usage.Cached = chunk.Usage.PromptDetails.Cached
			}
			response.Usage = model.Usage{InputTokens: chunk.Usage.Prompt, OutputTokens: chunk.Usage.Completion, CacheReadTokens: chunk.Usage.Cached}
			if sink != nil {
				usage := response.Usage
				if err := sink(model.StreamEvent{Type: model.StreamUsage, Usage: &usage}); err != nil {
					return err
				}
			}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if sink != nil {
					if err := sink(model.StreamEvent{Type: model.StreamTextDelta, Text: choice.Delta.Content}); err != nil {
						return err
					}
				}
			}
			if choice.Delta.Reasoning != "" {
				reasoning.WriteString(choice.Delta.Reasoning)
				if sink != nil {
					if err := sink(model.StreamEvent{Type: model.StreamReasoningDelta, Text: choice.Delta.Reasoning}); err != nil {
						return err
					}
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := calls[delta.Index]
				if call == nil {
					call = &model.ToolCall{}
					calls[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Function.Name != "" {
					call.Name = delta.Function.Name
				}
				if delta.Function.Arguments != "" {
					call.Arguments = append(call.Arguments, delta.Function.Arguments...)
				}
			}
			if choice.FinishReason != "" {
				response.FinishReason = finishReason(choice.FinishReason)
			}
		}
		return nil
	})
	if err != nil {
		return model.Response{}, classifyTransport(err)
	}
	response.Message.Role = model.RoleAssistant
	if reasoning.Len() > 0 {
		response.Message.Content = append(response.Message.Content, model.Content{Type: model.ContentReasoning, Text: reasoning.String()})
	}
	if text.Len() > 0 {
		response.Message.Content = append(response.Message.Content, model.Content{Type: model.ContentText, Text: text.String()})
	}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := calls[index]
		response.Message.Content = append(response.Message.Content, model.Content{Type: model.ContentToolCall, ToolCall: call})
		if sink != nil {
			if err := sink(model.StreamEvent{Type: model.StreamToolCallDone, ToolCall: call}); err != nil {
				return model.Response{}, err
			}
		}
	}
	if response.FinishReason == "" {
		if len(calls) > 0 {
			response.FinishReason = model.FinishToolCalls
		} else {
			response.FinishReason = model.FinishStop
		}
	}
	if sink != nil {
		_ = sink(model.StreamEvent{Type: model.StreamCompleted, ResponseID: response.ID, Usage: &response.Usage})
	}
	return response, nil
}

func encodeMessages(messages []model.Message) []chatMessage {
	encoded := make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		item := chatMessage{Role: string(message.Role)}
		var texts []string
		var images []string
		for _, block := range message.Content {
			switch block.Type {
			case model.ContentText:
				texts = append(texts, block.Text)
			case model.ContentImage:
				if block.ImageURL != "" {
					images = append(images, block.ImageURL)
				}
			case model.ContentToolCall:
				if block.ToolCall != nil {
					call := chatToolCall{ID: block.ToolCall.ID, Type: "function"}
					call.Function.Name = block.ToolCall.Name
					call.Function.Arguments = string(block.ToolCall.Arguments)
					item.ToolCalls = append(item.ToolCalls, call)
				}
			case model.ContentToolResult:
				if block.ToolResult != nil {
					encoded = append(encoded, chatMessage{Role: "tool", ToolCallID: block.ToolResult.CallID, Content: contentText(block.ToolResult.Content)})
				}
			}
		}
		if len(images) > 0 {
			parts := make([]map[string]any, 0, len(texts)+len(images))
			for _, text := range texts {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
			for _, image := range images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}})
			}
			item.Content = parts
		} else if len(texts) > 0 {
			item.Content = strings.Join(texts, "")
		}
		if item.Content != nil || len(item.ToolCalls) > 0 || item.Role == "system" || item.Role == "user" {
			encoded = append(encoded, item)
		}
	}
	return encoded
}

func encodeTools(definitions []model.ToolDefinition) []map[string]any {
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		var schema any
		_ = json.Unmarshal(definition.InputSchema, &schema)
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": definition.Name, "description": definition.Description, "parameters": schema}})
	}
	return tools
}

func contentText(content []model.Content) string {
	var value strings.Builder
	for _, block := range content {
		if block.Type == model.ContentText {
			value.WriteString(block.Text)
		} else if block.Type == model.ContentJSON {
			value.Write(block.JSON)
		}
	}
	return value.String()
}

func finishReason(value string) model.FinishReason {
	switch value {
	case "tool_calls":
		return model.FinishToolCalls
	case "length":
		return model.FinishLength
	case "content_filter":
		return model.FinishContent
	case "stop":
		return model.FinishStop
	default:
		return model.FinishError
	}
}

func classifyTransport(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &model.Error{Kind: model.ErrorTransient, Message: err.Error(), Retryable: true, Cause: err}
}

func decodeHTTPError(response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	kind, retryable := model.ErrorInvalid, false
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		kind = model.ErrorAuth
	}
	if response.StatusCode == http.StatusTooManyRequests {
		kind, retryable = model.ErrorRateLimited, true
	}
	if response.StatusCode >= 500 {
		kind, retryable = model.ErrorUnavailable, true
	}
	return &model.Error{Kind: kind, Message: fmt.Sprintf("model endpoint returned %s: %s", response.Status, strings.TrimSpace(string(data))), Retryable: retryable, StatusCode: response.StatusCode}
}
