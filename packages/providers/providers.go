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
	ResponsesModels []string
}

// Client is the sole wire-to-canonical model adapter used by every profile.
type Client struct{ Wire llm.Client }

func New(config Config) (*Client, error) {
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	protocol := strings.ToLower(strings.TrimSpace(config.Protocol))
	if protocol == "" {
		return nil, fmt.Errorf("model protocol is required")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Minute
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("model base URL is required")
	}
	var wire llm.Client
	switch protocol {
	case "anthropic":
		wire = llm.NewAnthropicClient(config.BaseURL, config.APIKey, config.Timeout, config.AnthropicFlavor)
	case "responses":
		wire = llm.NewOpenAIResponsesClient(provider, config.BaseURL, config.APIKey, config.Timeout)
	case "openai":
		openAI := llm.NewOpenAICompatibleClient(provider, config.BaseURL, config.APIKey, config.Timeout)
		if len(config.ResponsesModels) > 0 {
			responses := llm.NewOpenAIResponsesClient(provider, config.BaseURL, config.APIKey, config.Timeout)
			wire = llm.NewProtocolRouter(openAI, responses, config.ResponsesModels)
		} else {
			wire = openAI
		}
	default:
		return nil, fmt.Errorf("unsupported model protocol %q", config.Protocol)
	}
	return &Client{Wire: wire}, nil
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
	var conversionErr error
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	publish := func(event model.StreamEvent) {
		if sink == nil {
			return
		}
		sinkMu.Lock()
		defer sinkMu.Unlock()
		if sinkErr == nil {
			sinkErr = sink(event)
			if sinkErr != nil {
				cancelStream()
			}
		}
	}
	if stream, ok := c.Wire.(llm.StreamClient); ok {
		publish(model.StreamEvent{Type: model.StreamStarted})
		response, err = stream.ChatStream(streamCtx, req, llm.StreamHandler{
			OnText: func(delta string) {
				if delta != "" {
					publish(model.StreamEvent{Type: model.StreamTextDelta, Text: delta})
				}
			},
			OnToolCallComplete: func(call llm.ToolCall) {
				canonical, convertErr := toToolCall(call)
				if convertErr != nil {
					conversionErr = convertErr
					return
				}
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
	if conversionErr != nil {
		return model.Response{}, conversionErr
	}
	if sinkErr != nil {
		return model.Response{}, sinkErr
	}
	if err != nil {
		return model.Response{}, toModelError(err)
	}
	message, err := fromWireMessage(response.Message)
	if err != nil {
		return model.Response{}, err
	}
	reason := finishReason(response.FinishReason)
	if reason == model.FinishError {
		return model.Response{}, &model.Error{
			Kind:      model.ErrorTransient,
			Message:   fmt.Sprintf("provider finished with error reason %q", response.FinishReason),
			Retryable: true,
		}
	}
	canonical := model.Response{Message: message, FinishReason: reason, Usage: toUsage(response.Usage), RawMetadata: response.Raw}
	publish(model.StreamEvent{Type: model.StreamCompleted, ResponseID: canonical.ID, Usage: &canonical.Usage})
	return canonical, nil
}

func toWireMessage(message model.Message) llm.Message {
	out := llm.Message{Role: string(message.Role)}
	var parts []llm.ContentPart
	hasImage := false
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			out.Content += block.Text
			if block.Text != "" {
				parts = append(parts, llm.TextPart(block.Text))
			}
		case model.ContentImage:
			if strings.TrimSpace(block.ImageURL) != "" {
				hasImage = true
				parts = append(parts, llm.ImageURLPart(block.ImageURL))
			}
		case model.ContentReasoning:
			out.ReasoningContent += block.Text
		case model.ContentJSON:
			out.Content += string(block.JSON)
			if len(block.JSON) > 0 {
				parts = append(parts, llm.TextPart(string(block.JSON)))
			}
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
	// Multimodal wire formats represent the complete message as content parts.
	// Keeping text only in Content would make MarshalJSON drop it as soon as an
	// image part is present.
	if hasImage && out.Role != "tool" {
		out.ContentParts = parts
		out.Content = ""
	}
	return out
}

func fromWireMessage(message llm.Message) (model.Message, error) {
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
		canonical, err := toToolCall(call)
		if err != nil {
			return model.Message{}, err
		}
		out.Content = append(out.Content, model.Content{Type: model.ContentToolCall, ToolCall: &canonical})
	}
	return out, nil
}

func toToolCall(call llm.ToolCall) (model.ToolCall, error) {
	if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
		return model.ToolCall{}, fmt.Errorf("provider returned an incomplete tool call")
	}
	arguments := json.RawMessage(call.Function.Arguments)
	if !json.Valid(arguments) {
		return model.ToolCall{}, fmt.Errorf("provider returned invalid JSON arguments for tool %q", call.Function.Name)
	}
	return model.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments}, nil
}

func toUsage(usage llm.Usage) model.Usage {
	return model.Usage{InputTokens: int64(usage.PromptTokens), OutputTokens: int64(usage.CompletionTokens), CacheReadTokens: int64(usage.CacheReadInputTokens), CacheCreatedTokens: int64(usage.CacheCreationInputTokens), CacheTokensIncludedInInput: usage.CacheIncludedInPrompt}
}

func finishReason(reason string) model.FinishReason {
	reason = strings.TrimSpace(reason)
	switch strings.ToLower(reason) {
	case "tool_calls", "tool_use":
		return model.FinishToolCalls
	case "length", "max_tokens", "incomplete":
		return model.FinishLength
	case "content_filter", "refusal":
		return model.FinishContent
	case "stop", "end_turn", "stop_sequence", "completed":
		return model.FinishStop
	case "canceled", "cancelled":
		return model.FinishCanceled
	case "error", "failed":
		return model.FinishError
	case "network_error", "network", "connection_error", "timeout", "timed_out", "server_error", "internal_error", "overloaded", "overload":
		return model.FinishError
	case "":
		return model.FinishStop
	}
	// Providers extend finish_reason freely. Treat unknown terminal reasons as
	// provider-side failures instead of failing response conversion.
	return model.FinishError
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
