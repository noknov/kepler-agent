package llm

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAnthropicMessagesURL(t *testing.T) {
	tests := map[string]string{
		"https://api.kimi.com/coding/":   "https://api.kimi.com/coding/v1/messages",
		"https://api.kimi.com/coding/v1": "https://api.kimi.com/coding/v1/messages",
		"https://api.anthropic.com":      "https://api.anthropic.com/v1/messages",
	}
	for baseURL, want := range tests {
		if got := anthropicMessagesURL(baseURL); got != want {
			t.Fatalf("anthropicMessagesURL(%q) = %q, want %q", baseURL, got, want)
		}
	}
}

func TestAnthropicMessagesConvertToolCallsAndResults(t *testing.T) {
	messages := anthropicMessages([]Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "check logs"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID:   "toolu_1",
				Type: "function",
				Function: ToolFunction{
					Name:      "gcp-logs",
					Arguments: `{"service":"api"}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "toolu_1", Content: "log output"},
		{Role: "tool", ToolCallID: "toolu_2", Content: "more output"},
	})

	if len(messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3", len(messages))
	}
	if messages[1].Role != "assistant" || messages[1].Content[0].Type != "tool_use" {
		t.Fatalf("assistant tool call was not converted: %#v", messages[1])
	}
	if messages[2].Role != "user" || len(messages[2].Content) != 2 {
		t.Fatalf("tool results were not coalesced into one user message: %#v", messages[2])
	}
	if messages[2].Content[0].Type != "tool_result" || messages[2].Content[0].ToolUseID != "toolu_1" {
		t.Fatalf("first tool result mismatch: %#v", messages[2].Content[0])
	}
}

func TestAnthropicSystemBlocksSplitsDynamicContextForCache(t *testing.T) {
	system := "static rules\n\n---DYNAMIC_CONTEXT_BELOW---\n\ndynamic repo inventory"
	raw := anthropicSystemBlocks([]Message{{Role: "system", Content: system}})
	blocks, ok := raw.([]map[string]any)
	if !ok {
		t.Fatalf("system blocks type = %T", raw)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2: %#v", len(blocks), blocks)
	}
	if _, ok := blocks[0]["cache_control"]; !ok {
		t.Fatalf("static block should be cacheable: %#v", blocks[0])
	}
	if _, ok := blocks[1]["cache_control"]; ok {
		t.Fatalf("dynamic block should not be cacheable: %#v", blocks[1])
	}
}

func TestAnthropicMessagesConvertImageParts(t *testing.T) {
	messages := anthropicMessages([]Message{{
		Role: "user",
		ContentParts: []ContentPart{
			TextPart("describe this"),
			ImageURLPart("data:image/png;base64,aGVsbG8="),
		},
	}})

	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	blocks := messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "describe this" {
		t.Fatalf("text block mismatch: %#v", blocks[0])
	}
	source, ok := blocks[1].Source.(map[string]string)
	if blocks[1].Type != "image" || !ok {
		t.Fatalf("image block mismatch: %#v", blocks[1])
	}
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "aGVsbG8=" {
		t.Fatalf("image source mismatch: %#v", source)
	}
}

func TestAnthropicTools(t *testing.T) {
	tools := anthropicTools([]ToolSpec{{
		Type: "function",
		Function: ToolSpecFunction{
			Name:        "code-search",
			Description: "Search code",
			Parameters:  map[string]any{"type": "object"},
		},
	}})

	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"name":"code-search","description":"Search code","input_schema":{"type":"object"}}]`
	if string(data) != want {
		t.Fatalf("tools JSON = %s, want %s", data, want)
	}
}

func TestAnthropicAuthHeadersOfficial(t *testing.T) {
	header := http.Header{}
	setAnthropicAuthHeaders(header, "sk-ant-test", "official")

	if got := header.Get("x-api-key"); got != "sk-ant-test" {
		t.Fatalf("x-api-key = %q, want sk-ant-test", got)
	}
	if got := header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	if got := header.Get("x-app"); got != "" {
		t.Fatalf("x-app = %q, want empty", got)
	}
}

func TestAnthropicAuthHeadersClaudeCode(t *testing.T) {
	header := http.Header{}
	setAnthropicAuthHeaders(header, "sk-kimi-test", "claude-code")

	if got := header.Get("x-api-key"); got != "sk-kimi-test" {
		t.Fatalf("x-api-key = %q, want sk-kimi-test", got)
	}
	if got := header.Get("Authorization"); got != "Bearer sk-kimi-test" {
		t.Fatalf("Authorization = %q, want Bearer token", got)
	}
	if got := header.Get("x-app"); got != "cli" {
		t.Fatalf("x-app = %q, want cli", got)
	}
}
