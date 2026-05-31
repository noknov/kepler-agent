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

type KimiClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewKimiClient(baseURL, apiKey string, timeout time.Duration) *KimiClient {
	return &KimiClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *KimiClient) Chat(ctx Context, req Request) (Response, error) {
	payload, err := json.Marshal(c.chatBody(req))
	if err != nil {
		return Response{}, err
	}

	stdCtx, ok := ctx.(context.Context)
	if !ok {
		stdCtx = context.Background()
	}
	data, err := c.doWithRetry(stdCtx, payload)
	if err != nil {
		return Response{}, err
	}

	var parsed struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, err
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("kimi returned no choices")
	}
	return Response{
		Message:      parsed.Choices[0].Message,
		FinishReason: parsed.Choices[0].FinishReason,
		Usage:        parsed.Usage,
		Raw:          data,
	}, nil
}

func (c *KimiClient) chatBody(req Request) map[string]any {
	body := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"tools":       req.Tools,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}
	if len(req.Tools) == 0 {
		delete(body, "tools")
	}
	if isMiMoEndpoint(c.baseURL, req.Model) {
		delete(body, "max_tokens")
		body["max_completion_tokens"] = req.MaxTokens
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

func (c *KimiClient) doWithRetry(ctx context.Context, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		data, err := c.doOnce(ctx, payload)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !IsTemporaryOverload(err) || attempt == 3 {
			return nil, err
		}
		if err := sleepBeforeRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *KimiClient) doOnce(ctx context.Context, payload []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if hasBearerPrefix(c.apiKey) {
		httpReq.Header.Set("Authorization", c.apiKey)
	}
	if isMiMoEndpoint(c.baseURL, "") {
		httpReq.Header.Del("Authorization")
		httpReq.Header.Set("api-key", bearerTokenValue(c.apiKey))
	}
	httpReq.Header.Set("Content-Type", "application/json")

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
		return nil, ProviderError{Provider: "kimi chat completion", StatusCode: resp.StatusCode, Body: compactBody(data)}
	}
	return data, nil
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
