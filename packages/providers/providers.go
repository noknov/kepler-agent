// Package providers adapts model wire protocols to the canonical agent model.
// Local and hosted profiles use this same factory so harness evaluations cover
// the exact provider path used in production.
package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
	"github.com/noknov/kepler-agent/packages/llm/chatgpt"
)

type Config struct {
	ChatGPTCredentialsFile string
	Provider               string
	Protocol               string
	BaseURL                string
	APIKey                 string
	AnthropicFlavor        string
	Timeout                time.Duration
	OutputTokens           int
}

// Client is the sole wire-to-canonical model adapter used by every profile.
type Client struct {
	Wire llm.Client
	Host model.Client
}

const KeplerGeneratePath = "/v1/kepler/generate"

func New(config Config) (*Client, error) {
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	protocol := strings.ToLower(strings.TrimSpace(config.Protocol))
	responsesModels := responsesModels(provider)
	if protocol == "" {
		return nil, fmt.Errorf("model protocol is required")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Minute
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("model base URL is required")
	}
	if protocol == "kepler" {
		return &Client{Host: newKeplerRemote(config.BaseURL, config.APIKey, config.Timeout)}, nil
	}
	if provider == "qoder" || protocol == "qoder" {
		if provider != "qoder" || protocol != "qoder" {
			return nil, fmt.Errorf("qoder requires the native qoder protocol")
		}
		wire, err := llm.NewQoderClient(config.BaseURL, config.APIKey, config.Timeout)
		if err != nil {
			return nil, err
		}
		return &Client{Wire: wire}, nil
	}
	if provider == "chatgpt" {
		if protocol != "responses" || strings.TrimRight(config.BaseURL, "/") != chatgpt.Resource {
			return nil, fmt.Errorf("chatgpt requires the official Responses endpoint")
		}
		if _, err := chatgpt.Load(config.ChatGPTCredentialsFile); err != nil {
			return nil, err
		}
		responses := llm.NewOpenAIResponsesClient(provider, config.BaseURL, "", config.Timeout)
		responses.SetTransport(&chatgpt.Transport{Path: config.ChatGPTCredentialsFile})
		return &Client{Wire: responses}, nil
	}
	var wire llm.Client
	switch protocol {
	case "anthropic":
		wire = llm.NewAnthropicClient(config.BaseURL, config.APIKey, config.Timeout, config.AnthropicFlavor)
	case "responses":
		wire = llm.NewOpenAIResponsesClient(provider, config.BaseURL, config.APIKey, config.Timeout)
	case "openai":
		openAI := llm.NewOpenAICompatibleClient(provider, config.BaseURL, config.APIKey, config.Timeout)
		if len(responsesModels) > 0 {
			responses := llm.NewOpenAIResponsesClient(provider, config.BaseURL, config.APIKey, config.Timeout)
			wire = llm.NewProtocolRouter(openAI, responses, responsesModels)
		} else {
			wire = openAI
		}
	default:
		return nil, fmt.Errorf("unsupported model protocol %q", config.Protocol)
	}
	return &Client{Wire: wire}, nil
}

func (c *Client) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	if c != nil && c.Host != nil {
		response, err := c.Host.Generate(ctx, request, sink)
		return response, toModelError(err)
	}
	if c == nil || c.Wire == nil {
		return model.Response{}, fmt.Errorf("model provider is not configured")
	}
	req := llm.Request{
		Model: request.Model, MaxTokens: request.MaxOutputTokens,
		Thinking: request.ReasoningEffort, Temperature: request.Temperature,
		Metadata: request.Metadata,
	}
	for _, message := range request.Messages {
		req.Messages = append(req.Messages, toWireMessage(message))
	}
	for _, definition := range request.Tools {
		var parameters map[string]any
		if err := json.Unmarshal(definition.InputSchema, &parameters); err != nil {
			return model.Response{}, fmt.Errorf("decode tool schema for %s: %w", definition.Name, err)
		}
		normalizeJSONSchema(parameters)
		req.Tools = append(req.Tools, llm.ToolSpec{Type: "function", Function: llm.ToolSpecFunction{Name: definition.Name, Description: definition.Description, Parameters: parameters}})
	}
	var response llm.Response
	var err error
	var sinkMu sync.Mutex
	var sinkErr error
	var conversionErr error
	var firstToken, presentedText, presentedCommentary bool
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
	// Streaming exists to deliver incremental events. Callers without an event
	// sink do not benefit from an SSE connection and should use the provider's
	// ordinary completion endpoint instead.
	if stream, ok := c.Wire.(llm.StreamClient); ok && sink != nil {
		publish(model.StreamEvent{Type: model.StreamStarted})
		markFirstToken := func() {
			if !firstToken {
				firstToken = true
				publish(model.StreamEvent{Type: model.StreamFirstToken})
			}
		}
		response, err = stream.ChatStream(streamCtx, req, llm.StreamHandler{
			OnFirstToken: markFirstToken,
			OnCommentary: func(text string) {
				if text != "" {
					presentedCommentary = true
					publish(model.StreamEvent{Type: model.StreamCommentary, Text: text, Phase: "commentary"})
				}
			},
			OnText: func(delta llm.TextDelta) {
				if delta.Text == "" {
					return
				}
				markFirstToken()
				// Without phases, text preceding a tool call is ambiguous. Plain
				// chat has no tool definitions and keeps its live streaming; tool-
				// enabled responses are classified after completion. Explicitly
				// phased final answers continue streaming immediately.
				if delta.Phase != "" && delta.Phase != "final_answer" {
					return
				}
				if delta.Phase == "" && len(req.Tools) > 0 {
					return
				}
				presentedText = true
				publish(model.StreamEvent{
					Type:   model.StreamTextDelta,
					Text:   delta.Text,
					ItemID: delta.ItemID,
					Phase:  delta.Phase,
				})
			},
			OnToolCallsStarted: markFirstToken,
			OnToolCallComplete: func(call llm.ToolCall) {
				markFirstToken()
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
		return model.Response{Usage: toUsage(response.Usage)}, toModelError(err)
	}
	message, err := fromWireMessage(response.Message)
	if err != nil {
		return model.Response{}, err
	}
	message.Phase = response.Message.Phase
	if len(message.ToolCalls()) > 0 {
		message = toolCallStepMessage(message)
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
	if !presentedCommentary && len(message.ToolCalls()) > 0 && response.Message.Content != "" {
		publish(model.StreamEvent{Type: model.StreamCommentary, Text: response.Message.Content, Phase: "commentary"})
	}
	if !presentedText && len(message.ToolCalls()) == 0 &&
		(message.Phase == "" || message.Phase == "final_answer") &&
		(reason == model.FinishStop || reason == model.FinishLength || reason == model.FinishContent) && message.Text() != "" {
		publish(model.StreamEvent{Type: model.StreamTextDelta, Text: message.Text(), Phase: "final_answer"})
	}
	if sinkErr != nil {
		return model.Response{}, sinkErr
	}
	publish(model.StreamEvent{Type: model.StreamCompleted, ResponseID: canonical.ID, Usage: &canonical.Usage})
	return canonical, nil
}

// normalizeJSONSchema enforces the JSON Schema wire contract at the one
// provider-neutral boundary shared by every model protocol. Remote MCP
// descriptors may bypass the local ObjectSchema constructor. Only locations
// defined by JSON Schema to contain schemas are traversed, so examples,
// defaults, and vendor metadata remain byte-for-byte semantically unchanged.
func normalizeJSONSchema(schema map[string]any) {
	normalizeSchema(schema)
}

func normalizeSchema(schema map[string]any) {
	if schema == nil {
		return
	}
	if properties, exists := schema["properties"]; exists {
		switch properties := properties.(type) {
		case nil:
			schema["properties"] = map[string]any{}
		case map[string]any:
			normalizeSchemaMap(properties)
		}
	}

	for _, key := range []string{
		"additionalItems", "additionalProperties", "contains", "contentSchema",
		"else", "if", "items", "not", "propertyNames", "then",
		"unevaluatedItems", "unevaluatedProperties",
	} {
		normalizeSchemaValue(schema[key])
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		normalizeSchemaList(schema[key])
	}
	for _, key := range []string{"$defs", "definitions", "dependentSchemas", "patternProperties"} {
		normalizeSchemaMapValue(schema[key])
	}

	// Draft-07 uses dependencies for both property-name arrays and schemas.
	// Only map values are schemas; arrays are dependency names and are left as-is.
	if dependencies, ok := schema["dependencies"].(map[string]any); ok {
		for _, dependency := range dependencies {
			normalizeSchemaValue(dependency)
		}
	}
}

func normalizeSchemaValue(value any) {
	switch value := value.(type) {
	case map[string]any:
		normalizeSchema(value)
	case []any:
		normalizeSchemaList(value)
	}
}

func normalizeSchemaList(value any) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		normalizeSchemaValue(item)
	}
}

func normalizeSchemaMapValue(value any) {
	items, ok := value.(map[string]any)
	if !ok {
		return
	}
	normalizeSchemaMap(items)
}

func normalizeSchemaMap(items map[string]any) {
	for _, item := range items {
		normalizeSchemaValue(item)
	}
}

// toolCallStepMessage marks public narration without reclassifying it as native
// reasoning. It remains ordinary assistant text in subsequent provider input.
func toolCallStepMessage(message model.Message) model.Message {
	content := make([]model.Content, 0, len(message.Content))
	for _, block := range message.Content {
		switch block.Type {
		case model.ContentText:
			if strings.TrimSpace(block.Text) != "" {
				block.Phase = "commentary"
				content = append(content, block)
			}
		case model.ContentReasoning, model.ContentToolCall:
			content = append(content, block)
		}
	}
	message.Content = content
	if message.Phase == "" {
		message.Phase = "commentary"
	}
	return message
}

func toWireMessage(message model.Message) llm.Message {
	out := llm.Message{Role: string(message.Role), Phase: message.Phase}
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
						parts = append(parts, llm.TextPart(content.Text))
					} else if content.Type == model.ContentJSON {
						out.Content += string(content.JSON)
						parts = append(parts, llm.TextPart(string(content.JSON)))
					} else if content.Type == model.ContentImage && content.ImageURL != "" {
						hasImage = true
						parts = append(parts, llm.ImageURLPart(content.ImageURL))
					}
				}
			}
		}
	}
	// Multimodal wire formats represent the complete message as content parts.
	// Keeping text only in Content would make MarshalJSON drop it as soon as an
	// image part is present.
	if hasImage && out.Role == "tool" {
		out.ContentParts = parts
	} else if hasImage {
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
		return model.ToolCall{}, &model.Error{Kind: model.ErrorProtocol, Message: "provider returned an incomplete tool call", Retryable: true}
	}
	arguments := json.RawMessage(call.Function.Arguments)
	var object map[string]json.RawMessage
	if json.Unmarshal(arguments, &object) != nil || object == nil {
		fingerprint := sha256.Sum256(arguments)
		return model.ToolCall{}, &model.Error{
			Kind:      model.ErrorProtocol,
			Message:   fmt.Sprintf("provider returned invalid JSON arguments for tool %q (bytes=%d sha256_prefix=%x)", call.Function.Name, len(arguments), fingerprint[:8]),
			Retryable: true,
		}
	}
	return model.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments}, nil
}

func toUsage(usage llm.Usage) model.Usage {
	return model.Usage{Reported: usage.Reported, InputTokens: int64(usage.PromptTokens), OutputTokens: int64(usage.CompletionTokens), CacheReadTokens: int64(usage.CacheReadInputTokens), CacheCreatedTokens: int64(usage.CacheCreationInputTokens), CacheTokensIncludedInInput: usage.CacheIncludedInPrompt, ReasoningTokens: int64(usage.ReasoningTokens)}
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
	var typed *model.Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &model.Error{Kind: model.ErrorTransient, Message: err.Error(), Retryable: true, Cause: err}
	}
	var emptyErr llm.EmptyResponseError
	if errors.As(err, &emptyErr) && (emptyErr.Retryable || emptyErr.Provider == "qoder") {
		return &model.Error{Kind: model.ErrorProtocol, Message: err.Error(), Retryable: true, Cause: err}
	}
	var protocolErr llm.ProtocolError
	if errors.As(err, &protocolErr) {
		return &model.Error{Kind: model.ErrorProtocol, Message: err.Error(), Retryable: true, Cause: err}
	}
	var providerErr llm.ProviderError
	if errors.As(err, &providerErr) {
		kind := model.ErrorInvalid
		retryable := providerErr.Retryable()
		switch {
		case llm.ContextLimitBody(providerErr.Body):
			kind, retryable = model.ErrorContextLimit, false
		case llm.ContentPolicyBody(providerErr.Body):
			kind, retryable = model.ErrorContentPolicy, false
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
