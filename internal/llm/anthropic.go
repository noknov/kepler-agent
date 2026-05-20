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

func (c *AnthropicClient) Chat(ctx Context, req Request) (Response, error) {
	body := anthropicRequest{
		Model:       req.Model,
		System:      anthropicSystem(req.Messages),
		Messages:    anthropicMessages(req.Messages),
		Tools:       anthropicTools(req.Tools),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if len(body.Tools) == 0 {
		body.Tools = nil
	}

	payload, err := json.Marshal(body)
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
		return Response{}, fmt.Errorf("anthropic messages returned no text or tool calls")
	}

	return Response{
		Message:      message,
		FinishReason: parsed.StopReason,
		Usage: Usage{
			PromptTokens:     parsed.Usage.InputTokens,
			CompletionTokens: parsed.Usage.OutputTokens,
			TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
		},
		Raw: data,
	}, nil
}

func (c *AnthropicClient) doWithRetry(ctx context.Context, payload []byte) ([]byte, error) {
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
		return nil, ProviderError{Provider: "anthropic messages", StatusCode: resp.StatusCode, Body: compactBody(data)}
	}
	return data, nil
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
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

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	Content []anthropicBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
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

func anthropicSystem(messages []Message) string {
	parts := make([]string, 0)
	for _, msg := range messages {
		if msg.Role == "system" && strings.TrimSpace(msg.Content) != "" {
			parts = append(parts, msg.Content)
		}
	}
	return strings.Join(parts, "\n\n")
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

func anthropicMessagesURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/messages"
	}
	return baseURL + "/v1/messages"
}

func setAnthropicAuthHeaders(header http.Header, token, flavor string) {
	flavor = strings.ToLower(strings.TrimSpace(flavor))
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
		header.Set("User-Agent", "claude-cli/1.0 oncall-agent")
	}
}
