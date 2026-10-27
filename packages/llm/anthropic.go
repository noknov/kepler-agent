package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type AnthropicClient struct {
	baseURL    string
	apiKey     string
	flavor     string
	httpClient *http.Client
}

func NewAnthropicClient(baseURL, apiKey string, timeout time.Duration, flavor string) *AnthropicClient {
	flavor = strings.ToLower(strings.TrimSpace(flavor))
	if flavor == "" {
		flavor = "official"
	}
	return &AnthropicClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  apiKey,
		flavor:  flavor,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *AnthropicClient) Chat(ctx context.Context, req Request) (Response, error) {
	body := anthropicRequest{
		Model:       req.Model,
		System:      anthropicSystemBlocks(req.Messages),
		Messages:    anthropicMessages(req.Messages),
		Tools:       anthropicToolsCached(req.Tools),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Thinking:    anthropicThinkingParam(req.Thinking),
	}
	if req.OutputFormat != nil {
		body.OutputConfig = map[string]any{"format": map[string]any{"type": "json_schema", "schema": req.OutputFormat.Schema}}
	}
	if len(body.Tools) == 0 {
		body.Tools = nil
	} else if req.ToolChoice != "" {
		body.ToolChoice = map[string]string{"type": req.ToolChoice}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}

	data, err := c.doOnce(ctx, payload)
	if err != nil {
		return Response{}, err
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, err
	}
	message := Message{Role: "assistant", Content: strings.TrimSpace(parsed.text())}
	for _, block := range parsed.Content {
		if block.Type != "tool_use" {
			continue
		}
		args := "{}"
		if len(block.Input) > 0 {
			args = string(block.Input)
		}
		message.ToolCalls = append(message.ToolCalls, ToolCall{
			ID:   block.ID,
			Type: "function",
			Function: ToolFunction{
				Name:      block.Name,
				Arguments: args,
			},
		})
	}
	if message.Content == "" && len(message.ToolCalls) == 0 {
		return Response{
				Usage: anthropicUsage(parsed),
				Raw:   data,
			}, EmptyResponseError{
				Provider:     "anthropic messages",
				StopReason:   parsed.StopReason,
				ContentTypes: parsed.contentTypes(),
				PromptTokens: parsed.Usage.InputTokens,
				OutputTokens: parsed.Usage.OutputTokens,
			}
	}

	return Response{
		Message:      message,
		FinishReason: parsed.StopReason,
		Usage:        anthropicUsage(parsed),
		Raw:          data,
	}, nil
}

func (c *AnthropicClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	body := anthropicRequest{
		Model:       req.Model,
		System:      anthropicSystemBlocks(req.Messages),
		Messages:    anthropicMessages(req.Messages),
		Tools:       anthropicToolsCached(req.Tools),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
		Thinking:    anthropicThinkingParam(req.Thinking),
	}
	if req.OutputFormat != nil {
		body.OutputConfig = map[string]any{"format": map[string]any{"type": "json_schema", "schema": req.OutputFormat.Schema}}
	}
	if len(body.Tools) == 0 {
		body.Tools = nil
	} else if req.ToolChoice != "" {
		body.ToolChoice = map[string]string{"type": req.ToolChoice}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	setAnthropicAuthHeaders(httpReq.Header, c.apiKey, c.flavor)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return Response{}, NewProviderError("anthropic stream", resp.StatusCode, compactBody(data))
	}

	var msg Message
	msg.Role = "assistant"
	var stopReason string
	var usage anthropicUsageDetails
	type contentState struct {
		kind, text string
		call       *ToolCall
		closed     bool
	}
	blocks := map[int]*contentState{}
	messageStopped := false
	var streamErr error

	err = readSSE(resp.Body, func(ev sseEvent) bool {
		switch ev.Event {
		case "error":
			var failure struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(ev.Data), &failure) == nil && failure.Error.Message != "" {
				streamErr = fmt.Errorf("anthropic stream %s: %s", failure.Error.Type, failure.Error.Message)
			} else {
				streamErr = fmt.Errorf("anthropic stream error")
			}
			return false
		case "content_block_start":
			var block struct {
				Index        *int `json:"index"`
				ContentBlock struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content_block"`
			}
			if json.Unmarshal([]byte(ev.Data), &block) != nil || block.Index == nil || *block.Index < 0 || block.ContentBlock.Type == "" {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "invalid content block start"}
				return false
			}
			if _, exists := blocks[*block.Index]; exists {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "duplicate content block index"}
				return false
			}
			state := &contentState{kind: block.ContentBlock.Type, text: block.ContentBlock.Text}
			if state.kind == "tool_use" {
				if strings.TrimSpace(block.ContentBlock.ID) == "" || strings.TrimSpace(block.ContentBlock.Name) == "" {
					streamErr = ProtocolError{Provider: "anthropic", Detail: "missing tool identity"}
					return false
				}
				args := ""
				if input := strings.TrimSpace(string(block.ContentBlock.Input)); input != "" && input != "{}" {
					args = string(block.ContentBlock.Input)
				}
				state.call = &ToolCall{ID: block.ContentBlock.ID, Type: "function", Function: ToolFunction{Name: block.ContentBlock.Name, Arguments: args}}
				if h.OnToolCallsStarted != nil {
					h.OnToolCallsStarted()
				}
			} else if state.kind == "text" && state.text != "" && h.OnText != nil {
				h.OnText(TextDelta{Text: state.text})
			}
			blocks[*block.Index] = state
		case "content_block_delta":
			var delta struct {
				Index *int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &delta) != nil || delta.Index == nil {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "invalid content block delta"}
				return false
			}
			state := blocks[*delta.Index]
			if state == nil || state.closed {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "delta references no open content block"}
				return false
			}
			switch delta.Delta.Type {
			case "text_delta":
				if state.kind != "text" {
					streamErr = ProtocolError{Provider: "anthropic", Detail: "text delta references a non-text block"}
					return false
				}
				state.text += delta.Delta.Text
				if delta.Delta.Text != "" && h.OnText != nil {
					h.OnText(TextDelta{Text: delta.Delta.Text})
				}
			case "input_json_delta":
				if state.call != nil {
					state.call.Function.Arguments += delta.Delta.PartialJSON
				} else if state.kind == "text" {
					streamErr = ProtocolError{Provider: "anthropic", Detail: "input delta references a text block"}
					return false
				}
			}
		case "content_block_stop":
			var stop struct {
				Index *int `json:"index"`
			}
			if json.Unmarshal([]byte(ev.Data), &stop) != nil || stop.Index == nil {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "invalid content block stop"}
				return false
			}
			state := blocks[*stop.Index]
			if state == nil || state.closed {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "stop references no open content block"}
				return false
			}
			state.closed = true
			if state.call != nil {
				if state.call.Function.Arguments == "" {
					state.call.Function.Arguments = "{}"
				}
				var input map[string]json.RawMessage
				if json.Unmarshal([]byte(state.call.Function.Arguments), &input) != nil || input == nil {
					streamErr = ProtocolError{Provider: "anthropic", Detail: "completed tool input is not a JSON object"}
					return false
				}
				if h.OnToolCallComplete != nil {
					h.OnToolCallComplete(*state.call)
				}
			}
		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(ev.Data), &md) == nil {
				if md.Delta.StopReason != "" {
					stopReason = md.Delta.StopReason
				}
				if md.Usage.OutputTokens > 0 {
					usage.OutputTokens = md.Usage.OutputTokens
					if h.OnUsage != nil {
						h.OnUsage(Usage{
							Reported:                 usage.Reported,
							PromptTokens:             usage.InputTokens,
							CompletionTokens:         usage.OutputTokens,
							TotalTokens:              usage.InputTokens + usage.OutputTokens,
							CacheReadInputTokens:     usage.CacheReadInputTokens,
							CacheCreationInputTokens: usage.CacheCreationInputTokens,
						})
					}
				}
			} else {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "invalid message delta"}
				return false
			}

		case "message_start":
			var ms struct {
				Message struct {
					Usage anthropicUsageDetails `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(ev.Data), &ms) == nil {
				usage.Reported = ms.Message.Usage.Reported
				usage.InputTokens = ms.Message.Usage.InputTokens
				usage.CacheReadInputTokens = ms.Message.Usage.CacheReadInputTokens
				usage.CacheCreationInputTokens = ms.Message.Usage.CacheCreationInputTokens
				if h.OnUsage != nil && usage.Reported {
					h.OnUsage(Usage{
						Reported:                 usage.Reported,
						PromptTokens:             usage.InputTokens,
						CompletionTokens:         usage.OutputTokens,
						TotalTokens:              usage.InputTokens + usage.OutputTokens,
						CacheReadInputTokens:     usage.CacheReadInputTokens,
						CacheCreationInputTokens: usage.CacheCreationInputTokens,
					})
				}
			} else {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "invalid message start"}
				return false
			}

		case "message_stop":
			if !json.Valid([]byte(ev.Data)) || strings.TrimSpace(stopReason) == "" {
				streamErr = ProtocolError{Provider: "anthropic", Detail: "missing message completion evidence"}
				return false
			}
			for _, state := range blocks {
				if !state.closed {
					streamErr = ProtocolError{Provider: "anthropic", Detail: "message stopped with an open content block"}
					return false
				}
			}
			messageStopped = true
			return false
		}
		return true
	})
	if streamErr != nil {
		return Response{}, streamErr
	}
	if err != nil {
		return Response{}, err
	}
	if !messageStopped {
		return Response{}, ProtocolError{Provider: "anthropic", Detail: "stream ended without message_stop"}
	}
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		state := blocks[index]
		if state.kind == "text" {
			msg.Content += state.text
		}
		if state.call != nil {
			msg.ToolCalls = append(msg.ToolCalls, *state.call)
		}
	}

	return Response{
		Message:      msg,
		FinishReason: stopReason,
		Usage: Usage{
			Reported:                 usage.Reported,
			PromptTokens:             usage.InputTokens,
			CompletionTokens:         usage.OutputTokens,
			TotalTokens:              usage.InputTokens + usage.OutputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
		},
		Streamed: true,
	}, nil
}

func (c *AnthropicClient) doOnce(ctx context.Context, payload []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	setAnthropicAuthHeaders(httpReq.Header, c.apiKey, c.flavor)

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
		return nil, NewProviderError("anthropic messages", resp.StatusCode, compactBody(data))
	}
	return data, nil
}

type anthropicRequest struct {
	OutputConfig any                `json:"output_config,omitempty"`
	Model        string             `json:"model"`
	System       any                `json:"system,omitempty"`
	Messages     []anthropicMessage `json:"messages"`
	Tools        []anthropicTool    `json:"tools,omitempty"`
	ToolChoice   any                `json:"tool_choice,omitempty"`
	MaxTokens    int                `json:"max_tokens,omitempty"`
	Temperature  *float64           `json:"temperature,omitempty"`
	Stream       bool               `json:"stream,omitempty"`
	Thinking     *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Source       any             `json:"source,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      any             `json:"content,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl *cacheControl  `json:"cache_control,omitempty"`
}

type anthropicResponse struct {
	Content    []anthropicBlock      `json:"content"`
	Usage      anthropicUsageDetails `json:"usage"`
	StopReason string                `json:"stop_reason"`
}

func (r anthropicResponse) text() string {
	parts := make([]string, 0)
	for _, block := range r.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (r anthropicResponse) contentTypes() []string {
	types := make([]string, 0, len(r.Content))
	for _, block := range r.Content {
		if block.Type != "" {
			types = append(types, block.Type)
		}
	}
	return types
}

func anthropicUsage(r anthropicResponse) Usage {
	return Usage{
		Reported:                 r.Usage.Reported,
		PromptTokens:             r.Usage.InputTokens,
		CompletionTokens:         r.Usage.OutputTokens,
		TotalTokens:              r.Usage.InputTokens + r.Usage.OutputTokens,
		CacheReadInputTokens:     r.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: r.Usage.CacheCreationInputTokens,
	}
}

// anthropicSystemBlocks builds the system prompt as cacheable content blocks.
// The last block is marked with cache_control to enable Anthropic's prompt
// caching — subsequent requests with the same system prefix hit the cache
// and skip re-encoding, reducing TTFT by ~80%.
func anthropicSystemBlocks(messages []Message) any {
	blocks := make([]map[string]any, 0)
	for _, msg := range messages {
		if msg.Role != "system" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		for _, part := range splitSystemPromptForCache(msg.Content) {
			if strings.TrimSpace(part.text) == "" {
				continue
			}
			block := map[string]any{
				"type": "text",
				"text": part.text,
			}
			if part.cacheable {
				block["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	markLastCacheableBlock(blocks)
	return blocks
}

type systemPromptPart struct {
	text      string
	cacheable bool
}

func splitSystemPromptForCache(text string) []systemPromptPart {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	const marker = "---DYNAMIC_CONTEXT_BELOW---"
	idx := strings.Index(text, marker)
	if idx < 0 {
		return []systemPromptPart{{text: text, cacheable: true}}
	}
	staticPart := strings.TrimSpace(text[:idx])
	dynamicPart := strings.TrimSpace(text[idx:])
	parts := make([]systemPromptPart, 0, 2)
	if staticPart != "" {
		parts = append(parts, systemPromptPart{text: staticPart, cacheable: true})
	}
	if dynamicPart != "" {
		parts = append(parts, systemPromptPart{text: dynamicPart})
	}
	return parts
}

func markLastCacheableBlock(blocks []map[string]any) {
	for i := len(blocks) - 1; i >= 0; i-- {
		if _, ok := blocks[i]["cache_control"]; ok {
			return
		}
	}
	if len(blocks) > 0 {
		blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
	}
}

func anthropicMessages(messages []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			continue
		case "assistant":
			blocks := make([]anthropicBlock, 0, 1+len(msg.ToolCalls))
			blocks = append(blocks, anthropicContentBlocks(msg)...)
			for _, call := range msg.ToolCalls {
				input := json.RawMessage(call.Function.Arguments)
				if !json.Valid(input) {
					input, _ = json.Marshal(map[string]string{"arguments": call.Function.Arguments})
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    call.ID,
					Name:  call.Function.Name,
					Input: input,
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			}
		case "tool":
			block := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			if len(msg.ContentParts) > 0 {
				block.Content = anthropicContentBlocks(msg)
			}
			last := len(out) - 1
			if last >= 0 && out[last].Role == "user" && onlyToolResults(out[last].Content) {
				out[last].Content = append(out[last].Content, block)
			} else {
				out = append(out, anthropicMessage{Role: "user", Content: []anthropicBlock{block}})
			}
		default:
			blocks := anthropicContentBlocks(msg)
			if len(blocks) > 0 {
				out = append(out, anthropicMessage{
					Role:    "user",
					Content: blocks,
				})
			}
		}
	}
	markAnthropicConversationCacheBreakpoint(out)
	return out
}

// markAnthropicConversationCacheBreakpoint places an ephemeral cache breakpoint
// on the last block of the most recent user/tool turn so multi-step agent loops
// reuse the encoded prefix across consecutive tool iterations.
func markAnthropicConversationCacheBreakpoint(messages []anthropicMessage) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" || len(messages[i].Content) == 0 {
			continue
		}
		last := len(messages[i].Content) - 1
		messages[i].Content[last].CacheControl = &cacheControl{Type: "ephemeral"}
		return
	}
}

func anthropicContentBlocks(msg Message) []anthropicBlock {
	if len(msg.ContentParts) == 0 {
		if strings.TrimSpace(msg.Content) == "" {
			return nil
		}
		return []anthropicBlock{{Type: "text", Text: msg.Content}}
	}
	blocks := make([]anthropicBlock, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		switch part.Type {
		case "text":
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: part.Text})
			}
		case "image_url":
			if part.ImageURL == nil || strings.TrimSpace(part.ImageURL.URL) == "" {
				continue
			}
			source, ok := anthropicImageSource(part.ImageURL.URL)
			if !ok {
				continue
			}
			blocks = append(blocks, anthropicBlock{Type: "image", Source: source})
		}
	}
	return blocks
}

func anthropicImageSource(dataURL string) (map[string]string, bool) {
	const marker = ";base64,"
	if !strings.HasPrefix(dataURL, "data:image/") {
		return nil, false
	}
	idx := strings.Index(dataURL, marker)
	if idx < 0 {
		return nil, false
	}
	mediaType := strings.TrimPrefix(dataURL[:idx], "data:")
	data := dataURL[idx+len(marker):]
	if mediaType == "" || data == "" {
		return nil, false
	}
	return map[string]string{
		"type":       "base64",
		"media_type": mediaType,
		"data":       data,
	}, true
}

func onlyToolResults(blocks []anthropicBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}

func anthropicTools(tools []ToolSpec) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}
	return out
}

// anthropicToolsCached marks the last tool with cache_control so that the
// full tool schema list is cached by Anthropic. The tool registry uses stable
// sorted ordering (sort.Strings on names) ensuring cache hits across requests.
func anthropicToolsCached(tools []ToolSpec) []anthropicTool {
	out := anthropicTools(tools)
	if len(out) > 0 {
		out[len(out)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	return out
}

func anthropicMessagesURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/messages"
	}
	return baseURL + "/v1/messages"
}

// anthropicThinkingParam converts the Request.Thinking hint into the Anthropic
// API thinking object. Only "disabled" is handled explicitly; other values
// (empty string, "enabled") are left to the model/server default.
func anthropicThinkingParam(thinking string) *anthropicThinking {
	if strings.ToLower(strings.TrimSpace(thinking)) == "disabled" {
		return &anthropicThinking{Type: "disabled"}
	}
	return nil
}

func setAnthropicAuthHeaders(header http.Header, token, flavor string) {
	flavor = strings.ToLower(strings.TrimSpace(flavor))
	header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	if hasBearerPrefix(token) {
		header.Set("Authorization", token)
		if flavor != "claude-code" {
			header.Set("x-api-key", strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(token, "Bearer "), "bearer ")))
		}
		return
	}
	header.Set("x-api-key", token)
	if flavor == "claude-code" {
		header.Set("Authorization", "Bearer "+token)
		header.Set("x-app", "cli")
		header.Set("User-Agent", "claude-cli/1.0 kepler-agent")
	}
}

type anthropicUsageDetails struct {
	Reported                 bool `json:"-"`
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens"`
}

func (u *anthropicUsageDetails) UnmarshalJSON(data []byte) error {
	type plain anthropicUsageDetails
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*u = anthropicUsageDetails(decoded)
	u.Reported = (len(fields["input_tokens"]) > 0 && string(fields["input_tokens"]) != "null") || (len(fields["output_tokens"]) > 0 && string(fields["output_tokens"]) != "null")
	return nil
}
