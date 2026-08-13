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
	var usage struct {
		InputTokens              int
		OutputTokens             int
		CacheReadInputTokens     int
		CacheCreationInputTokens int
	}
	inTextBlock := false
	toolBlocks := map[int]*ToolCall{}
	currentBlockIndex := -1
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
				Index        int `json:"index"`
				ContentBlock struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content_block"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &block)
			currentBlockIndex = block.Index
			inTextBlock = block.ContentBlock.Type == "text"
			if block.ContentBlock.Type == "tool_use" {
				if h.OnToolCallsStarted != nil {
					h.OnToolCallsStarted()
				}
				args := ""
				if input := strings.TrimSpace(string(block.ContentBlock.Input)); input != "" && input != "{}" {
					args = string(block.ContentBlock.Input)
				}
				toolBlocks[block.Index] = &ToolCall{
					ID:   block.ContentBlock.ID,
					Type: "function",
					Function: ToolFunction{
						Name:      block.ContentBlock.Name,
						Arguments: args,
					},
				}
			}
		case "content_block_delta":
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &delta) != nil {
				return true
			}
			switch {
			case inTextBlock && delta.Delta.Type == "text_delta" && delta.Delta.Text != "":
				msg.Content += delta.Delta.Text
				if h.OnText != nil {
					h.OnText(delta.Delta.Text)
				}
			case delta.Delta.Type == "input_json_delta" && delta.Delta.PartialJSON != "":
				call := toolBlocks[delta.Index]
				if call == nil && currentBlockIndex >= 0 {
					call = toolBlocks[currentBlockIndex]
				}
				if call != nil {
					call.Function.Arguments += delta.Delta.PartialJSON
				}
			}
		case "content_block_stop":
			if call := toolBlocks[currentBlockIndex]; call != nil {
				if call.Function.Arguments == "" {
					call.Function.Arguments = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, *call)
				if h.OnToolCallComplete != nil {
					h.OnToolCallComplete(*call)
				}
			}
			inTextBlock = false
			currentBlockIndex = -1
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
				stopReason = md.Delta.StopReason
				if md.Usage.OutputTokens > 0 {
					usage.OutputTokens = md.Usage.OutputTokens
					if h.OnUsage != nil {
						h.OnUsage(Usage{
							PromptTokens:             usage.InputTokens,
							CompletionTokens:         usage.OutputTokens,
							TotalTokens:              usage.InputTokens + usage.OutputTokens,
							CacheReadInputTokens:     usage.CacheReadInputTokens,
							CacheCreationInputTokens: usage.CacheCreationInputTokens,
						})
					}
				}
			}
		case "message_start":
			var ms struct {
				Message struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(ev.Data), &ms) == nil {
				usage.InputTokens = ms.Message.Usage.InputTokens
				usage.CacheReadInputTokens = ms.Message.Usage.CacheReadInputTokens
				usage.CacheCreationInputTokens = ms.Message.Usage.CacheCreationInputTokens
				if h.OnUsage != nil {
					h.OnUsage(Usage{
						PromptTokens:             usage.InputTokens,
						CompletionTokens:         usage.OutputTokens,
						TotalTokens:              usage.InputTokens + usage.OutputTokens,
						CacheReadInputTokens:     usage.CacheReadInputTokens,
						CacheCreationInputTokens: usage.CacheCreationInputTokens,
					})
				}
			}
		case "message_stop":
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
	return Response{
		Message:      msg,
		FinishReason: stopReason,
		Usage: Usage{
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
	Model       string             `json:"model"`
	System      any                `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    any             `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
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
	Content []anthropicBlock `json:"content"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	StopReason string `json:"stop_reason"`
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
	return out
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
		header.Set("User-Agent", "claude-cli/1.0 slack-copilot-agent")
	}
}
