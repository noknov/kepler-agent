package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiMoChatBodyUsesOfficialOpenAIFields(t *testing.T) {
	client := NewKimiClient("https://api.xiaomimimo.com/v1", "token", 0)
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

func TestBearerTokenValue(t *testing.T) {
	if got := bearerTokenValue("Bearer sk-test"); got != "sk-test" {
		t.Fatalf("bearerTokenValue() = %q, want sk-test", got)
	}
	if got := bearerTokenValue("sk-test"); got != "sk-test" {
		t.Fatalf("bearerTokenValue() = %q, want sk-test", got)
	}
}

func TestProviderName(t *testing.T) {
	if got := NewKimiClient("https://api.xiaomimimo.com/v1", "token", 0).providerName(); got != "mimo" {
		t.Fatalf("providerName() = %q, want mimo", got)
	}
	if got := NewKimiClient("https://api.moonshot.ai/v1", "token", 0).providerName(); got != "kimi" {
		t.Fatalf("providerName() = %q, want kimi", got)
	}
}

func TestKimiChatStreamParsesToolCallDeltas(t *testing.T) {
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

	client := NewKimiClient(server.URL, "token", 0)
	client.httpClient = server.Client()

	var streamed string
	resp, err := client.ChatStream(context.Background(), Request{
		Model: "test",
		Tools: []ToolSpec{{Type: "function", Function: ToolSpecFunction{
			Name:       "echo",
			Parameters: map[string]any{"type": "object"},
		}}},
	}, func(delta string) { streamed += delta })
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if streamed != "" {
		t.Fatalf("streamed text = %q, want empty for tool-only response", streamed)
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
