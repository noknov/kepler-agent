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
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"echo\",\"arguments\":\"\\\"ok\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n"))
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

func TestOpenAICompatibleChatStreamDefaultsFinishReasonAfterCleanEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient("test", server.URL, "token", 0)
	resp, err := client.ChatStream(context.Background(), Request{Model: "test"}, StreamHandler{})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop", resp.FinishReason)
	}
}

func TestOpenAICompatibleChatStreamParsesCumulativeToolCallSnapshots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"code-search\",\"arguments\":\"{\\\"query\\\":\\\"hel\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"code-search\",\"arguments\":\"{\\\"query\\\":\\\"hello\\\",\\\"source\\\":\\\"working_tree\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient("opencode-go", server.URL, "token", 0)
	response, err := client.ChatStream(context.Background(), Request{Model: "test"}, StreamHandler{})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", response.FinishReason)
	}
	calls := response.Message.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("ToolCalls = %#v, want one", calls)
	}
	if call := calls[0]; call.Function.Name != "code-search" || call.Function.Arguments != `{"query":"hello","source":"working_tree"}` {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestOpenAICompatibleChatStreamRejectsInvalidCumulativeSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"code-search\",\"arguments\":\"{\\\"query\\\":\\\"hel\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"code-search\",\"arguments\":\"lo\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient("opencode-go", server.URL, "token", 0)
	if _, err := client.ChatStream(context.Background(), Request{Model: "test"}, StreamHandler{}); err == nil {
		t.Fatal("ChatStream() error = nil, want cumulative snapshot protocol error")
	}
}

func TestMergeToolArguments(t *testing.T) {
	tests := []struct {
		name     string
		mode     toolArgumentMode
		previous string
		update   string
		want     string
		wantErr  bool
	}{
		{name: "delta first fragment", mode: toolArgumentsDelta, update: `{"query":"`, want: `{"query":"`},
		{name: "delta next fragment", mode: toolArgumentsDelta, previous: `{"query":"`, update: `hello"}`, want: `{"query":"hello"}`},
		{name: "delta valid update is still a fragment", mode: toolArgumentsDelta, previous: `{"items":[`, update: `1`, want: `{"items":[1`},
		{name: "cumulative first snapshot", mode: toolArgumentsCumulative, update: `{"query":"hel`, want: `{"query":"hel`},
		{name: "cumulative repeated snapshot", mode: toolArgumentsCumulative, previous: `{"query":"hel`, update: `{"query":"hel`, want: `{"query":"hel`},
		{name: "cumulative extended snapshot", mode: toolArgumentsCumulative, previous: `{"query":"hel`, update: `{"query":"hello"}`, want: `{"query":"hello"}`},
		{name: "cumulative non-prefix rejected", mode: toolArgumentsCumulative, previous: `{"query":"hel`, update: `lo"}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeToolArguments(test.previous, test.update, test.mode)
			if test.wantErr {
				if err == nil {
					t.Fatalf("mergeToolArguments() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("mergeToolArguments() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("mergeToolArguments() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompletedOpenAICompatibleFinishReason(t *testing.T) {
	tests := []struct {
		name    string
		reason  string
		message Message
		want    string
	}{
		{name: "preserves provider reason", reason: "length", want: "length"},
		{name: "completed text", message: Message{Content: "done"}, want: "stop"},
		{name: "completed tool call", message: Message{ToolCalls: []ToolCall{{ID: "call_1"}}}, want: "tool_calls"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := completedOpenAICompatibleFinishReason(test.reason, test.message); got != test.want {
				t.Fatalf("completedOpenAICompatibleFinishReason(%q, %#v) = %q, want %q", test.reason, test.message, got, test.want)
			}
		})
	}
}
