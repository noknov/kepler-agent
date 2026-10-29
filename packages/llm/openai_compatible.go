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

	data, err := c.doOnce(ctx, payload, req.Metadata)
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
	Reported         bool `json:"-"`
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	// DeepSeek reports cache reads as prompt_cache_hit_tokens and mirrors them
	// in prompt_tokens_details.cached_tokens. Reading both keeps cache
	// accounting correct if either field is ever dropped.
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *openAIUsage) UnmarshalJSON(data []byte) error {
	type plain openAIUsage
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*u = openAIUsage(decoded)
	u.Reported = (len(fields["prompt_tokens"]) > 0 && string(fields["prompt_tokens"]) != "null") || (len(fields["completion_tokens"]) > 0 && string(fields["completion_tokens"]) != "null")
	return nil
}

func (u openAIUsage) toUsage() Usage {
	// For OpenAI-compatible APIs, prompt_tokens_details.cached_tokens is a
	// *subset* of prompt_tokens (not an independent field like Anthropic's
	// cache_read_input_tokens). We set CacheIncludedInPrompt=true so that
	// token counters know not to add CacheReadInputTokens a second time.
	cached := u.PromptTokensDetails.CachedTokens
	if cached == 0 {
		cached = u.PromptCacheHitTokens
	}
	return Usage{
		Reported:              u.Reported,
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CacheReadInputTokens:  cached,
		ReasoningTokens:       u.CompletionTokensDetails.ReasoningTokens,
		CacheIncludedInPrompt: true,
	}
}

type openAIDialect struct {
	useMaxCompletionTokens bool
	apiKeyHeader           string
	thinkingFormatter      func(string) map[string]string
}

func resolveOpenAIDialect(provider string) openAIDialect {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "mimo":
		return openAIDialect{
			useMaxCompletionTokens: true,
			apiKeyHeader:           "api-key",
			thinkingFormatter:      genericThinkingParam,
		}
	case "deepseek":
		return openAIDialect{
			thinkingFormatter: deepseekThinkingParam,
		}
	default:
		return openAIDialect{
			thinkingFormatter: genericThinkingParam,
		}
	}
}

func genericThinkingParam(thinking string) map[string]string {
	if thinking == "enabled" || thinking == "disabled" {
		return map[string]string{"type": thinking}
	}
	return nil
}

func (c *OpenAICompatibleClient) chatBody(req Request) map[string]any {
	dialect := resolveOpenAIDialect(c.providerName())
	body := map[string]any{
		"model":    req.Model,
		"messages": chatMessagesWithToolImages(req.Messages),
		"tools":    req.Tools,
	}
	if req.OutputFormat != nil {
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": req.OutputFormat.Name, "schema": req.OutputFormat.Schema, "strict": true}}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		if dialect.useMaxCompletionTokens {
			body["max_completion_tokens"] = req.MaxTokens
		} else {
			body["max_tokens"] = req.MaxTokens
		}
	}
	if len(req.Tools) == 0 {
		delete(body, "tools")
	} else {
		body["parallel_tool_calls"] = true
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}
	if dialect.thinkingFormatter != nil {
		if thinking := dialect.thinkingFormatter(req.Thinking); thinking != nil {
			body["thinking"] = thinking
		}
	}
	return body
}

// deepseekThinkingParam converts the Request.Thinking hint into DeepSeek's
// thinking object. Unlike the generic enabled/disabled switch, DeepSeek also
// accepts reasoning_effort (none|low|high|max) to bound how much chain of
// thought is generated before the final answer, which matters because
// reasoning tokens share the max_tokens budget with the answer.
func deepseekThinkingParam(thinking string) map[string]string {
	switch strings.ToLower(strings.TrimSpace(thinking)) {
	case "":
		return nil
	case "disabled", "none":
		return map[string]string{"type": "disabled"}
	case "enabled":
		return map[string]string{"type": "enabled"}
	case "low", "high", "max":
		return map[string]string{"type": "enabled", "reasoning_effort": strings.ToLower(strings.TrimSpace(thinking))}
	case "medium", "xhigh":
		return map[string]string{"type": "enabled", "reasoning_effort": "high"}
	case "minimal":
		return map[string]string{"type": "enabled", "reasoning_effort": "low"}
	default:
		return nil
	}
}

func (c *OpenAICompatibleClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	body := c.chatBody(req)
	body["stream"] = true
	// Request per-stream usage stats so token counts are accurate even when
	// the provider does not include them in the final delta by default.
	body["stream_options"] = map[string]any{"include_usage": true}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	c.setHeaders(httpReq, req.Metadata)

	// Reuse the configured transport and request deadline. A fresh timeout-free
	// client bypasses transport settings and can leave a stream hung forever.
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		providerErr := NewProviderError(c.providerName()+" stream", resp.StatusCode, string(data))
		return Response{}, providerErr
	}

	var msg Message
	msg.Role = "assistant"
	var finishReason string
	var usage openAIUsage
	toolCallsStarted := false
	var streamErr error

	err = readSSE(resp.Body, func(ev sseEvent) bool {
		if ev.Data == "[DONE]" {
			return false
		}
		var envelope struct {
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(ev.Data), &envelope) == nil && envelope.Error != nil {
			streamErr = fmt.Errorf("%s stream %s: %s", c.providerName(), envelope.Error.Type, envelope.Error.Message)
			return false
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
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
			streamErr = ProtocolError{Provider: c.providerName(), Detail: "invalid stream event JSON"}
			return false
		}
		if chunk.Usage.Reported {
			usage = chunk.Usage
			if h.OnUsage != nil {
				h.OnUsage(usage.toUsage())
			}
		}
		if len(chunk.Choices) == 0 {
			return true
		}
		delta := chunk.Choices[0].Delta
		if finishReason != "" && (delta.Content != "" || delta.ReasoningContent != "" || len(delta.ToolCalls) > 0 || chunk.Choices[0].FinishReason != nil) {
			streamErr = ProtocolError{Provider: c.providerName(), Detail: "response content changed after finish reason"}
			return false
		}
		if delta.Content != "" {
			msg.Content += delta.Content
			if h.OnText != nil {
				h.OnText(TextDelta{Text: delta.Content})
			}
		}
		if delta.ReasoningContent != "" {
			msg.ReasoningContent += delta.ReasoningContent
		}
		for _, tc := range delta.ToolCalls {
			if tc.Index == nil || *tc.Index < 0 || *tc.Index >= maxStreamToolCalls {
				streamErr = ProtocolError{Provider: c.providerName(), Detail: "missing or out-of-budget tool call index"}
				return false
			}
			index := *tc.Index
			for len(msg.ToolCalls) <= index {
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{Type: "function"})
			}
			call := &msg.ToolCalls[index]
			if (tc.ID != "" && call.ID != "" && tc.ID != call.ID) ||
				(tc.Function.Name != "" && call.Function.Name != "" && tc.Function.Name != call.Function.Name) {
				streamErr = ProtocolError{Provider: c.providerName(), Detail: "tool call identity changed within one index"}
				return false
			}
			if !toolCallsStarted && (tc.ID != "" || tc.Function.Name != "" || tc.Function.Arguments != "") {
				toolCallsStarted = true
				if h.OnToolCallsStarted != nil {
					h.OnToolCallsStarted()
				}
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Type != "" {
				call.Type = tc.Type
			}
			if tc.Function.Name != "" {
				call.Function.Name = tc.Function.Name
			}
			call.Function.Arguments += tc.Function.Arguments
		}
		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
		}
		return true
	})
	if streamErr != nil {
		return Response{}, streamErr
	}
	if err != nil {
		return Response{}, err
	}
	// A provider finish reason is completion evidence. EOF or [DONE] alone
	// cannot distinguish a completed response from a truncated one.
	if strings.TrimSpace(finishReason) == "" {
		return Response{}, ProtocolError{Provider: c.providerName(), Detail: "stream ended without a finish reason"}
	}
	if h.OnToolCallComplete != nil {
		for _, tc := range msg.ToolCalls {
			if strings.TrimSpace(tc.Function.Name) != "" {
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

func (c *OpenAICompatibleClient) setHeaders(httpReq *http.Request, metadata map[string]string) {
	dialect := resolveOpenAIDialect(c.providerName())
	if dialect.apiKeyHeader != "" {
		httpReq.Header.Set(dialect.apiKeyHeader, bearerTokenValue(c.apiKey))
	} else if hasBearerPrefix(c.apiKey) {
		httpReq.Header.Set("Authorization", c.apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setOpenCodeSessionHeader(httpReq, c.providerName(), metadata)
}

func (c *OpenAICompatibleClient) doOnce(ctx context.Context, payload []byte, metadata map[string]string) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq, metadata)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return readJSONResponse(resp, c.providerName()+" chat completion")
}

func (c *OpenAICompatibleClient) providerName() string {
	if c.provider != "" {
		return c.provider
	}
	return "openai-compatible"
}

func hasBearerPrefix(token string) bool {
	return len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ")
}

func bearerTokenValue(token string) string {
	token = strings.TrimSpace(token)
	if hasBearerPrefix(token) {
		return strings.TrimSpace(token[7:])
	}
	return token
}
