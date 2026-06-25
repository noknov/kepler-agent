package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiMoChatBodyUsesOfficialOpenAIFields(t *testing.T) {
	client := NewOpenAICompatibleClient("mimo", "https://api.xiaomimimo.com/v1", "token", 0)
	body := client.chatBody(Request{
		Model:     "mimo-v2.5",
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 1234,
		Thinking:  "disabled",
	})

	if _, ok := body["max_tokens"]; ok {
		t.Fatal("MiMo request should not use max_tokens")
	}
	if got := body["max_completion_tokens"]; got != 1234 {
		t.Fatalf("max_completion_tokens = %#v, want 1234", got)
	}
	thinking, ok := body["thinking"].(map[string]string)
	if !ok {
		t.Fatalf("thinking = %#v, want map[string]string", body["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled", thinking["type"])
	}
}

func TestChatBodyOmitsMaxTokensWhenUnset(t *testing.T) {
	client := NewOpenAICompatibleClient("opencode-go", "https://opencode.ai/zen/go/v1", "token", 0)
	body := client.chatBody(Request{
		Model:    "glm-5.2",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})

	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("chatBody should omit max_tokens when MaxTokens is unset: %#v", body)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Fatalf("chatBody should omit max_completion_tokens when MaxTokens is unset: %#v", body)
	}
}

func TestDeepSeekChatBodyUsesOpenAICompatibleToolCalls(t *testing.T) {
	client := NewOpenAICompatibleClient("deepseek", "https://api.deepseek.com", "token", 0)
	body := client.chatBody(Request{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools: []ToolSpec{{Type: "function", Function: ToolSpecFunction{
			Name:        "echo",
			Description: "Echo text",
			Parameters:  map[string]any{"type": "object"},
		}}},
		MaxTokens:   1234,
		Temperature: 0,
	})

	if got := body["model"]; got != "deepseek-v4-flash" {
		t.Fatalf("model = %#v, want deepseek-v4-flash", got)
	}
	if got := body["max_tokens"]; got != 1234 {
		t.Fatalf("max_tokens = %#v, want 1234", got)
	}
	tools, ok := body["tools"].([]ToolSpec)
	if !ok {
		t.Fatalf("tools = %#v, want []ToolSpec", body["tools"])
	}
	if len(tools) != 1 || tools[0].Type != "function" || tools[0].Function.Name != "echo" {
		t.Fatalf("tools = %#v, want OpenAI-compatible function tool", tools)
	}
	if _, ok := body["strict"]; ok {
		t.Fatalf("chatBody should not set DeepSeek beta strict mode fields: %#v", body)
	}
}

func TestBearerTokenValue(t *testing.T) {
	if got := bearerTokenValue("Bearer sk-test"); got != "sk-test" {
		t.Fatalf("bearerTokenValue() = %q, want sk-test", got)
	}
	if got := bearerTokenValue("sk-test"); got != "sk-test" {
		t.Fatalf("bearerTokenValue() = %q, want sk-test", got)
	}
}

func TestProviderName(t *testing.T) {
	if got := NewOpenAICompatibleClient("mimo", "https://api.xiaomimimo.com/v1", "token", 0).providerName(); got != "mimo" {
		t.Fatalf("providerName() = %q, want mimo", got)
	}
	if got := NewOpenAICompatibleClient("kimi", "https://api.moonshot.ai/v1", "token", 0).providerName(); got != "kimi" {
		t.Fatalf("providerName() = %q, want kimi", got)
	}
	if got := NewOpenAICompatibleClient("opencode-go", "https://opencode.ai/zen/go/v1", "token", 0).providerName(); got != "opencode-go" {
		t.Fatalf("providerName() = %q, want opencode-go", got)
	}
	if got := NewOpenAICompatibleClient("opencode-zen", "https://opencode.ai/zen/v1", "token", 0).providerName(); got != "opencode-zen" {
		t.Fatalf("providerName() = %q, want opencode-zen", got)
	}
	if got := NewOpenAICompatibleClient("deepseek", "https://api.deepseek.com", "token", 0).providerName(); got != "deepseek" {
		t.Fatalf("providerName() = %q, want deepseek", got)
	}
	if got := NewOpenAICompatibleClient("", "https://example.test/v1", "token", 0).providerName(); got != "openai-compatible" {
		t.Fatalf("providerName() = %q, want openai-compatible", got)
	}
}

func TestOpenAICompatibleChatStreamParsesToolCallDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"text\\\":\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"ok\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient("test", server.URL, "token", 0)
	client.httpClient = server.Client()

	var streamed string
	var toolStarted bool
	resp, err := client.ChatStream(context.Background(), Request{
		Model: "test",
		Tools: []ToolSpec{{Type: "function", Function: ToolSpecFunction{
			Name:       "echo",
			Parameters: map[string]any{"type": "object"},
		}}},
	}, StreamHandler{
		OnText:             func(delta string) { streamed += delta },
		OnToolCallsStarted: func() { toolStarted = true },
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if streamed != "" {
		t.Fatalf("streamed text = %q, want empty for tool-only response", streamed)
	}
	if !toolStarted {
		t.Fatal("OnToolCallsStarted was not called")
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v, want one", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Type != "function" || call.Function.Name != "echo" || call.Function.Arguments != `{"text":"ok"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if resp.Usage.TotalTokens != 3 {
		t.Fatalf("usage total = %d, want 3", resp.Usage.TotalTokens)
	}
}
