// Package anthropic adapts Anthropic Messages-compatible APIs to model.Client.
package anthropic

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

func (c *Client) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	system, messages := encodeMessages(request.Messages)
	body := map[string]any{"model": request.Model, "messages": messages, "stream": true, "max_tokens": request.MaxOutputTokens}
	if request.MaxOutputTokens <= 0 {
		body["max_tokens"] = 16_384
	}
	if system != "" {
		body["system"] = system
	}
	if len(request.Tools) > 0 {
		body["tools"] = encodeTools(request.Tools)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return model.Response{}, err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/messages"
	if strings.HasSuffix(strings.TrimRight(c.BaseURL, "/"), "/v1") {
		endpoint = strings.TrimRight(c.BaseURL, "/") + "/messages"
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return model.Response{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	if c.APIKey != "" {
		httpRequest.Header.Set("x-api-key", c.APIKey)
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
	type partial struct {
		kind string
		text strings.Builder
		call *model.ToolCall
	}
	partials := make(map[int]*partial)
	err = sse.Read(httpResponse.Body, func(event sse.Event) error {
		var envelope struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string `json:"id"`
				Usage struct {
					Input       int64 `json:"input_tokens"`
					Output      int64 `json:"output_tokens"`
					CacheRead   int64 `json:"cache_read_input_tokens"`
					CacheCreate int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
				ID       string `json:"id"`
				Name     string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(event.Data, &envelope); err != nil {
			return err
		}
		switch envelope.Type {
		case "message_start":
			response.ID = envelope.Message.ID
			response.Usage.InputTokens = envelope.Message.Usage.Input
			response.Usage.CacheReadTokens = envelope.Message.Usage.CacheRead
			response.Usage.CacheCreatedTokens = envelope.Message.Usage.CacheCreate
		case "content_block_start":
			part := &partial{kind: envelope.ContentBlock.Type}
			if part.kind == "tool_use" {
				part.call = &model.ToolCall{ID: envelope.ContentBlock.ID, Name: envelope.ContentBlock.Name}
			}
			if envelope.ContentBlock.Text != "" {
				part.text.WriteString(envelope.ContentBlock.Text)
			}
			if envelope.ContentBlock.Thinking != "" {
				part.text.WriteString(envelope.ContentBlock.Thinking)
			}
			partials[envelope.Index] = part
		case "content_block_delta":
			part := partials[envelope.Index]
			if part == nil {
				return fmt.Errorf("anthropic delta for unknown content block %d", envelope.Index)
			}
			switch envelope.Delta.Type {
			case "text_delta":
				part.text.WriteString(envelope.Delta.Text)
				if sink != nil {
					if err := sink(model.StreamEvent{Type: model.StreamTextDelta, Text: envelope.Delta.Text}); err != nil {
						return err
					}
				}
			case "thinking_delta":
				part.text.WriteString(envelope.Delta.Thinking)
				if sink != nil {
					if err := sink(model.StreamEvent{Type: model.StreamReasoningDelta, Text: envelope.Delta.Thinking}); err != nil {
						return err
					}
				}
			case "input_json_delta":
				part.call.Arguments = append(part.call.Arguments, envelope.Delta.PartialJSON...)
			}
		case "message_delta":
			response.FinishReason = finishReason(envelope.Delta.StopReason)
			response.Usage.OutputTokens = envelope.Usage.Output
			if sink != nil {
				usage := response.Usage
				if err := sink(model.StreamEvent{Type: model.StreamUsage, Usage: &usage}); err != nil {
					return err
				}
			}
		case "error":
			return &model.Error{Kind: model.ErrorUnavailable, Message: envelope.Error.Message, Retryable: true}
		}
		return nil
	})
	if err != nil {
		return model.Response{}, classifyTransport(err)
	}
	response.Message.Role = model.RoleAssistant
	indexes := make([]int, 0, len(partials))
	for index := range partials {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		part := partials[index]
		switch part.kind {
		case "text":
			response.Message.Content = append(response.Message.Content, model.Content{Type: model.ContentText, Text: part.text.String()})
		case "thinking":
			response.Message.Content = append(response.Message.Content, model.Content{Type: model.ContentReasoning, Text: part.text.String()})
		case "tool_use":
			if len(part.call.Arguments) == 0 {
				part.call.Arguments = json.RawMessage(`{}`)
			}
			response.Message.Content = append(response.Message.Content, model.Content{Type: model.ContentToolCall, ToolCall: part.call})
			if sink != nil {
				if err := sink(model.StreamEvent{Type: model.StreamToolCallDone, ToolCall: part.call}); err != nil {
					return model.Response{}, err
				}
			}
		}
	}
	if response.FinishReason == "" {
		if len(response.Message.ToolCalls()) > 0 {
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

func encodeMessages(messages []model.Message) (string, []map[string]any) {
	var systems []string
	encoded := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == model.RoleSystem {
			systems = append(systems, contentText(message.Content))
			continue
		}
		role := string(message.Role)
		if message.Role == model.RoleTool {
			role = "user"
		}
		blocks := make([]map[string]any, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case model.ContentText:
				blocks = append(blocks, map[string]any{"type": "text", "text": block.Text})
			case model.ContentToolCall:
				if block.ToolCall != nil {
					var input any
					if json.Unmarshal(block.ToolCall.Arguments, &input) != nil {
						input = map[string]any{}
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": block.ToolCall.ID, "name": block.ToolCall.Name, "input": input})
				}
			case model.ContentToolResult:
				if block.ToolResult != nil {
					blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": block.ToolResult.CallID, "content": contentText(block.ToolResult.Content), "is_error": block.ToolResult.IsError})
				}
			}
		}
		if len(blocks) > 0 {
			encoded = append(encoded, map[string]any{"role": role, "content": blocks})
		}
	}
	return strings.Join(systems, "\n\n"), encoded
}

func encodeTools(definitions []model.ToolDefinition) []map[string]any {
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		var schema any
		_ = json.Unmarshal(definition.InputSchema, &schema)
		tools = append(tools, map[string]any{"name": definition.Name, "description": definition.Description, "input_schema": schema})
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
	case "tool_use":
		return model.FinishToolCalls
	case "max_tokens":
		return model.FinishLength
	case "end_turn", "stop_sequence":
		return model.FinishStop
	default:
		return model.FinishError
	}
}

func classifyTransport(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var typed *model.Error
	if errors.As(err, &typed) {
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
