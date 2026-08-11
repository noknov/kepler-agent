// Package providers adapts model wire protocols to the canonical agent model.
// Local and hosted profiles use this same factory so harness evaluations cover
// the exact provider path used in production.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type Config struct {
	Provider        string
	Protocol        string
	BaseURL         string
	APIKey          string
	AnthropicFlavor string
	Timeout         time.Duration
}

// Client is the sole wire-to-canonical model adapter used by every profile.
type Client struct{ Wire llm.Client }

func New(config Config) (*Client, error) {
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	protocol := strings.ToLower(strings.TrimSpace(config.Protocol))
	if protocol == "" {
		protocol = "openai"
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Minute
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = defaultBaseURL(provider, protocol)
	}
	var wire llm.Client
	switch {
	case provider == "longcat":
		wire = llm.NewLongCatClient(config.BaseURL, config.APIKey, config.Timeout)
	case provider == "opencode-go":
		wire = llm.NewOpenCodeGoClient(config.BaseURL, config.APIKey, config.Timeout)
	case protocol == "anthropic":
		wire = llm.NewAnthropicClient(config.BaseURL, config.APIKey, config.Timeout, config.AnthropicFlavor)
	case protocol == "responses":
		wire = llm.NewOpenAIResponsesClient(provider, config.BaseURL, config.APIKey, config.Timeout)
	case protocol == "openai":
		wire = llm.NewOpenAICompatibleClient(provider, config.BaseURL, config.APIKey, config.Timeout)
	default:
		return nil, fmt.Errorf("unsupported model protocol %q", config.Protocol)
	}
	return &Client{Wire: wire}, nil
}

func defaultBaseURL(provider, protocol string) string {
	switch provider {
	case "anthropic":
		return "https://api.anthropic.com"
	case "opencode-go":
		return "https://opencode.ai/zen/go/v1"
	case "longcat":
		return "https://api.longcat.chat/openai/v1"
	}
	if protocol == "anthropic" {
		return "https://api.anthropic.com"
	}
	return "https://api.openai.com/v1"
}

func (c *Client) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	if c == nil || c.Wire == nil {
		return model.Response{}, fmt.Errorf("model provider is not configured")
	}
	req := llm.Request{
		Model: request.Model, MaxTokens: request.MaxOutputTokens,
		Thinking: request.ReasoningEffort, Temperature: request.Temperature,
	}
	for _, message := range request.Messages {
		req.Messages = append(req.Messages, toWireMessage(message))
	}
	for _, definition := range request.Tools {
		var parameters map[string]any
		if err := json.Unmarshal(definition.InputSchema, &parameters); err != nil {
			return model.Response{}, fmt.Errorf("decode tool schema for %s: %w", definition.Name, err)
		}
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
	if stream, ok := c.Wire.(llm.StreamClient); ok {
		publish(model.StreamEvent{Type: model.StreamStarted})
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
		response, err = c.Wire.Chat(ctx, req)
	}
	if err != nil {
		return model.Response{}, toModelError(err)
	}
	if sinkErr != nil {
		return model.Response{}, sinkErr
	}
	canonical := model.Response{Message: fromWireMessage(response.Message), FinishReason: finishReason(response.FinishReason), Usage: toUsage(response.Usage), RawMetadata: response.Raw}
	publish(model.StreamEvent{Type: model.StreamCompleted, ResponseID: canonical.ID, Usage: &canonical.Usage})
	return canonical, nil
}

func toWireMessage(message model.Message) llm.Message {
	out := llm.Message{Role: string(message.Role)}
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			out.Content += block.Text
		case model.ContentImage:
			out.ContentParts = append(out.ContentParts, llm.ImageURLPart(block.ImageURL))
		case model.ContentReasoning:
			out.ReasoningContent += block.Text
		case model.ContentJSON:
			out.Content += string(block.JSON)
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
					} else if content.Type == model.ContentJSON {
						out.Content += string(content.JSON)
					}
				}
			}
		}
	}
	return out
}

func fromWireMessage(message llm.Message) model.Message {
	out := model.Message{Role: model.Role(message.Role)}
	if message.ReasoningContent != "" {
		out.Content = append(out.Content, model.Content{Type: model.ContentReasoning, Text: message.ReasoningContent})
	}
	if message.Content != "" {
		citations := make([]model.Citation, 0, len(message.Citations))
		for _, citation := range message.Citations {
			citations = append(citations, model.Citation{URL: citation.URL, Title: citation.Title, StartIndex: citation.StartIndex, EndIndex: citation.EndIndex})
		}
		out.Content = append(out.Content, model.Content{Type: model.ContentText, Text: message.Content, Citations: citations})
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

func toModelError(err error) error {
	if err == nil {
		return nil
	}
	var providerErr llm.ProviderError
	if errors.As(err, &providerErr) {
		kind := model.ErrorInvalid
		retryable := providerErr.Retryable()
		switch {
		case providerErr.StatusCode == 401 || providerErr.StatusCode == 403:
			kind = model.ErrorAuth
		case providerErr.StatusCode == 429:
			kind = model.ErrorRateLimited
		case llm.IsPromptTooLong(err):
			kind = model.ErrorContextLimit
		case providerErr.StatusCode >= 500:
			kind = model.ErrorUnavailable
		}
		return &model.Error{Kind: kind, Message: err.Error(), Retryable: retryable, StatusCode: providerErr.StatusCode, Cause: err}
	}
	if llm.IsTemporaryOverload(err) {
		return &model.Error{Kind: model.ErrorTransient, Message: err.Error(), Retryable: true, Cause: err}
	}
	return err
}

var _ model.Client = (*Client)(nil)
