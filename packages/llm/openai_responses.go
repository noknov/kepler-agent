package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAIResponsesClient struct {
	provider   string
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewOpenAIResponsesClient(provider, baseURL, apiKey string, timeout time.Duration) *OpenAIResponsesClient {
	return &OpenAIResponsesClient{
		provider: strings.TrimSpace(provider),
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *OpenAIResponsesClient) Chat(ctx context.Context, req Request) (Response, error) {
	payload, err := json.Marshal(c.responsesBody(req, false))
	if err != nil {
		return Response{}, err
	}
	data, err := c.doWithRetry(ctx, payload)
	if err != nil {
		return Response{}, err
	}
	var parsed responsesResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, err
	}
	msg := parsed.message()
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		resp := Response{
			Usage: parsed.Usage.toUsage(),
			Raw:   data,
		}
		return resp, EmptyResponseError{
			Provider:     c.providerName() + " responses",
			StopReason:   parsed.Status,
			ContentTypes: parsed.contentTypes(),
			PromptTokens: parsed.Usage.InputTokens,
			OutputTokens: parsed.Usage.OutputTokens,
		}
	}
	return Response{
		Message:      msg,
		FinishReason: parsed.Status,
		Usage:        parsed.Usage.toUsage(),
		Raw:          data,
	}, nil
}

func (c *OpenAIResponsesClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	payload, err := json.Marshal(c.responsesBody(req, true))
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	c.setHeaders(httpReq)

	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		providerErr := NewProviderError(c.providerName()+" responses stream", resp.StatusCode, compactBody(data), resp.Header.Get("Retry-After"))
		if isRetryableStatus(resp.StatusCode) {
			fallback, fallbackErr := c.Chat(ctx, req)
			if fallbackErr == nil {
				return fallback, nil
			}
		}
		return Response{}, providerErr
	}

	msg := Message{Role: "assistant"}
	var finishReason string
	var usage responsesUsage
	toolCallsStarted := false
	var streamErr error
	err = readSSE(resp.Body, func(ev sseEvent) bool {
		if ev.Data == "[DONE]" {
			return false
		}
		var typ struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(ev.Data), &typ) != nil {
			return true
		}
		switch typ.Type {
		case "response.output_text.delta":
			var delta struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &delta) == nil && delta.Delta != "" {
				msg.Content += delta.Delta
				if h.OnText != nil {
					h.OnText(delta.Delta)
				}
			}
		case "response.function_call_arguments.done":
			var call struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal([]byte(ev.Data), &call) == nil {
				if !toolCallsStarted {
					toolCallsStarted = true
					if h.OnToolCallsStarted != nil {
						h.OnToolCallsStarted()
					}
				}
				tc := ToolCall{
					ID:   call.CallID,
					Type: "function",
					Function: ToolFunction{
						Name:      call.Name,
						Arguments: call.Arguments,
					},
				}
				msg.ToolCalls = append(msg.ToolCalls, tc)
				if h.OnToolCallComplete != nil {
					h.OnToolCallComplete(tc)
				}
			}
		case "response.completed":
			var done struct {
				Response responsesResponse `json:"response"`
			}
			if json.Unmarshal([]byte(ev.Data), &done) == nil {
				finishReason = done.Response.Status
				usage = done.Response.Usage
				if msg.Content == "" && len(msg.ToolCalls) == 0 {
					msg = done.Response.message()
				}
				if h.OnUsage != nil {
					h.OnUsage(usage.toUsage())
				}
			}
		case "error":
			var apiErr struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(ev.Data), &apiErr) == nil && apiErr.Message != "" {
				streamErr = fmt.Errorf("openai responses stream error: %s", apiErr.Message)
				return false
			}
		}
		return true
	})
	if streamErr != nil {
		return Response{}, streamErr
	}
	if err != nil {
		if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
			fallback, fallbackErr := c.Chat(ctx, req)
			if fallbackErr == nil {
				return fallback, nil
			}
		}
		return Response{}, err
	}
	return Response{
		Message:      msg,
		FinishReason: finishReason,
		Usage:        usage.toUsage(),
		Streamed:     true,
	}, nil
}

func (c *OpenAIResponsesClient) responsesBody(req Request, stream bool) map[string]any {
	body := map[string]any{
		"model":       req.Model,
		"input":       responsesInput(req.Messages),
		"temperature": req.Temperature,
	}
	if stream {
		body["stream"] = true
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		body["tools"] = responsesTools(req.Tools)
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}
	return body
}

func responsesInput(messages []Message) []any {
	input := make([]any, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "tool":
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  msg.Content,
			})
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				input = append(input, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": []map[string]string{{"type": "output_text", "text": msg.Content}},
				})
			}
			for _, call := range msg.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		default:
			content := responsesInputContent(msg)
			if len(content) == 0 {
				continue
			}
			input = append(input, map[string]any{
				"type":    "message",
				"role":    msg.Role,
				"content": content,
			})
		}
	}
	return input
}

func responsesInputContent(msg Message) []map[string]any {
	if len(msg.ContentParts) == 0 {
		if msg.Content == "" {
			return nil
		}
		return []map[string]any{{"type": "input_text", "text": msg.Content}}
	}
	content := make([]map[string]any, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		switch part.Type {
		case "text":
			content = append(content, map[string]any{"type": "input_text", "text": part.Text})
		case "image_url":
			if part.ImageURL != nil {
				content = append(content, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL})
			}
		}
	}
	return content
}

func responsesTools(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  tool.Function.Parameters,
		})
	}
	return out
}

func (c *OpenAIResponsesClient) setHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if hasBearerPrefix(c.apiKey) {
		httpReq.Header.Set("Authorization", c.apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")
}

func (c *OpenAIResponsesClient) doWithRetry(ctx context.Context, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < MaxRetries; attempt++ {
		data, err := c.doOnce(ctx, payload)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !IsTemporaryOverload(err) || attempt == MaxRetries-1 {
			return nil, err
		}
		if err := sleepBeforeRetry(ctx, attempt, lastErr); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *OpenAIResponsesClient) doOnce(ctx context.Context, payload []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewProviderError(c.providerName()+" responses", resp.StatusCode, compactBody(data), resp.Header.Get("Retry-After"))
	}
	return data, nil
}

func (c *OpenAIResponsesClient) providerName() string {
	if c.provider != "" {
		return c.provider
	}
	return "openai-responses"
}

type responsesResponse struct {
	Status string            `json:"status"`
	Output []responsesOutput `json:"output"`
	Usage  responsesUsage    `json:"usage"`
}

type responsesOutput struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id"`
	Role      string                   `json:"role"`
	Content   []responsesOutputContent `json:"content"`
	CallID    string                   `json:"call_id"`
	Name      string                   `json:"name"`
	Arguments string                   `json:"arguments"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (r responsesResponse) message() Message {
	msg := Message{Role: "assistant"}
	for _, item := range r.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" && content.Text != "" {
					msg.Content += content.Text
				}
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: ToolFunction{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}
	return msg
}

func (r responsesResponse) contentTypes() []string {
	types := make([]string, 0, len(r.Output))
	for _, item := range r.Output {
		if item.Type != "" {
			types = append(types, item.Type)
		}
	}
	return types
}

func (u responsesUsage) toUsage() Usage {
	return Usage{
		PromptTokens:          u.InputTokens,
		CompletionTokens:      u.OutputTokens,
		TotalTokens:           u.TotalTokens,
		CacheReadInputTokens:  u.InputTokensDetails.CachedTokens,
		ReasoningTokens:       u.OutputTokensDetails.ReasoningTokens,
		CacheIncludedInPrompt: true,
	}
}

var _ Client = (*OpenAIResponsesClient)(nil)
var _ StreamClient = (*OpenAIResponsesClient)(nil)
