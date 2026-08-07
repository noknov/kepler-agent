package llm

import (
	"context"
	"strings"
	"time"
)

type OpenCodeGoClient struct {
	openaiCompat *OpenAICompatibleClient
	anthropic    *AnthropicClient
	responses    *OpenAIResponsesClient
}

func NewOpenCodeGoClient(baseURL, apiKey string, timeout time.Duration) *OpenCodeGoClient {
	return &OpenCodeGoClient{
		openaiCompat: NewOpenAICompatibleClient("opencode-go", baseURL, apiKey, timeout),
		anthropic:    NewAnthropicClient(baseURL, apiKey, timeout, "official"),
		responses:    NewOpenAIResponsesClient("opencode-go", baseURL, apiKey, timeout),
	}
}

func (c *OpenCodeGoClient) Chat(ctx context.Context, req Request) (Response, error) {
	if openCodeGoUsesResponses(req.Model) {
		return c.responses.Chat(ctx, req)
	}
	if openCodeGoUsesAnthropic(req.Model) {
		return c.anthropic.Chat(ctx, req)
	}
	return c.openaiCompat.Chat(ctx, req)
}

func (c *OpenCodeGoClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	if openCodeGoUsesResponses(req.Model) {
		return c.responses.ChatStream(ctx, req, h)
	}
	if openCodeGoUsesAnthropic(req.Model) {
		return c.anthropic.ChatStream(ctx, req, h)
	}
	return c.openaiCompat.ChatStream(ctx, req, h)
}

func openCodeGoUsesResponses(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "gpt-5.6-luna"
}

func openCodeGoUsesAnthropic(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "minimax-") || strings.HasPrefix(model, "qwen")
}
