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

type OpenAICompatibleClient struct {
	provider   string
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewOpenAICompatibleClient(provider, baseURL, apiKey string, timeout time.Duration) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		provider: strings.TrimSpace(provider),
		baseURL:  baseURL,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *OpenAICompatibleClient) Chat(ctx context.Context, req Request) (Response, error) {
	payload, err := json.Marshal(c.chatBody(req))
	if err != nil {
		return Response{}, err
	}

	data, err := c.doWithRetry(ctx, payload)
	if err != nil {
		return Response{}, err
	}

	var parsed struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, err
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("%s returned no choices", c.providerName())
	}
	return Response{
		Message:      parsed.Choices[0].Message,
		FinishReason: parsed.Choices[0].FinishReason,
		Usage:        parsed.Usage.toUsage(),
		Raw:          data,
	}, nil
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u openAIUsage) toUsage() Usage {
	// For OpenAI-compatible APIs, prompt_tokens_details.cached_tokens is a
	// *subset* of prompt_tokens (not an independent field like Anthropic's
	// cache_read_input_tokens). We set CacheIncludedInPrompt=true so that
	// token counters know not to add CacheReadInputTokens a second time.
	return Usage{
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CacheReadInputTokens:  u.PromptTokensDetails.CachedTokens,
		ReasoningTokens:       u.CompletionTokensDetails.ReasoningTokens,
		CacheIncludedInPrompt: true,
	}
}

func (c *OpenAICompatibleClient) chatBody(req Request) map[string]any {
	body := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"tools":       req.Tools,
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) == 0 {
		delete(body, "tools")
	}
	if isMiMoEndpoint(c.baseURL, req.Model) {
		delete(body, "max_tokens")
		if req.MaxTokens > 0 {
			body["max_completion_tokens"] = req.MaxTokens
		}
		if req.Thinking == "enabled" || req.Thinking == "disabled" {
			body["thinking"] = map[string]string{"type": req.Thinking}
		}
		return body
	}
	if req.Thinking == "enabled" || req.Thinking == "disabled" {
		body["thinking"] = map[string]string{"type": req.Thinking}
	}
	return body
}

func (c *OpenAICompatibleClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	body := c.chatBody(req)
	body["stream"] = true
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	c.setHeaders(httpReq)

	// Use a timeout-free client for streaming — the global client timeout
	// would kill long generations mid-stream. Context cancellation still works.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		providerErr := NewProviderError(c.providerName()+" stream", resp.StatusCode, compactBody(data), resp.Header.Get("Retry-After"))
		// Fallback: retry retryable stream errors as non-stream request.
		if isRetryableStatus(resp.StatusCode) {
			fallback, fallbackErr := c.Chat(ctx, req)
			if fallbackErr == nil {
				return fallback, nil
			}
		}
		return Response{}, providerErr
	}

	var msg Message
	msg.Role = "assistant"
	var finishReason string
	var usage openAIUsage
	toolCallsStarted := false
	// Track tool call completion state for OnToolCallComplete.
	completedToolIndices := map[int]bool{}

	err = readSSE(resp.Body, func(ev sseEvent) bool {
		if ev.Data == "[DONE]" {
			return false
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage openAIUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(ev.Data), &chunk) != nil {
			return true
		}
		if chunk.Usage.TotalTokens > 0 {
			usage = chunk.Usage
			if h.OnUsage != nil {
				h.OnUsage(usage.toUsage())
			}
		}
		if len(chunk.Choices) == 0 {
			return true
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			msg.Content += delta.Content
			if h.OnText != nil {
				h.OnText(delta.Content)
			}
		}
		if delta.ReasoningContent != "" {
			msg.ReasoningContent += delta.ReasoningContent
		}
		for _, tc := range delta.ToolCalls {
			if !toolCallsStarted && (tc.ID != "" || tc.Function.Name != "" || tc.Function.Arguments != "") {
				toolCallsStarted = true
				if h.OnToolCallsStarted != nil {
					h.OnToolCallsStarted()
				}
			}
			// A new tool call ID on an existing index means the previous call is complete.
			if tc.ID != "" && tc.Index < len(msg.ToolCalls) && msg.ToolCalls[tc.Index].ID != "" && msg.ToolCalls[tc.Index].ID != tc.ID {
				if h.OnToolCallComplete != nil && !completedToolIndices[tc.Index] {
					completedToolIndices[tc.Index] = true
					h.OnToolCallComplete(msg.ToolCalls[tc.Index])
				}
			}
			for len(msg.ToolCalls) <= tc.Index {
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{Type: "function"})
			}
			call := &msg.ToolCalls[tc.Index]
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Type != "" {
				call.Type = tc.Type
			}
			if call.Type == "" {
				call.Type = "function"
			}
			if tc.Function.Name != "" {
				call.Function.Name += tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				call.Function.Arguments += tc.Function.Arguments
			}
		}
		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
		}
		return true
	})
	if err != nil {
		// Stream broke mid-way with no content — fallback to non-stream.
		if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
			fallback, fallbackErr := c.Chat(ctx, req)
			if fallbackErr == nil {
				return fallback, nil
			}
		}
		return Response{}, err
	}
	// Emit OnToolCallComplete for all completed tool calls at stream end.
	if h.OnToolCallComplete != nil {
		for i, tc := range msg.ToolCalls {
			if !completedToolIndices[i] && strings.TrimSpace(tc.Function.Name) != "" {
				h.OnToolCallComplete(tc)
			}
		}
	}
	return Response{
		Message:      msg,
		FinishReason: finishReason,
		Usage:        usage.toUsage(),
		Streamed:     true,
	}, nil
}

func (c *OpenAICompatibleClient) setHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if hasBearerPrefix(c.apiKey) {
		httpReq.Header.Set("Authorization", c.apiKey)
	}
	if isMiMoEndpoint(c.baseURL, "") {
		httpReq.Header.Del("Authorization")
		httpReq.Header.Set("api-key", bearerTokenValue(c.apiKey))
	}
	httpReq.Header.Set("Content-Type", "application/json")
}

func (c *OpenAICompatibleClient) doWithRetry(ctx context.Context, payload []byte) ([]byte, error) {
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

func (c *OpenAICompatibleClient) doOnce(ctx context.Context, payload []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
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
		return nil, NewProviderError(c.providerName()+" chat completion", resp.StatusCode, compactBody(data), resp.Header.Get("Retry-After"))
	}
	return data, nil
}

func (c *OpenAICompatibleClient) providerName() string {
	if c.provider != "" {
		return c.provider
	}
	return "openai-compatible"
}

func hasBearerPrefix(token string) bool {
	return len(token) >= 7 && (token[:7] == "Bearer " || token[:7] == "bearer ")
}

func bearerTokenValue(token string) string {
	token = strings.TrimSpace(token)
	if hasBearerPrefix(token) {
		return strings.TrimSpace(token[7:])
	}
	return token
}

func isMiMoEndpoint(baseURL, model string) bool {
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(baseURL, "xiaomimimo.com") || strings.HasPrefix(model, "mimo-")
}
