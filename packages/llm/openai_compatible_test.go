package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestChatBodyEnablesParallelToolCalls(t *testing.T) {
	client := NewOpenAICompatibleClient("opencode-go", "https://opencode.ai/zen/go/v1", "token", 0)
	body := client.chatBody(Request{
		Model:    "glm-5.2",
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools:    []ToolSpec{{Type: "function", Function: ToolSpecFunction{Name: "read", Parameters: map[string]any{"type": "object"}}}},
	})
	if body["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true", body["parallel_tool_calls"])
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
	if _, ok := body["temperature"]; ok {
		t.Fatalf("chatBody should omit temperature when unset: %#v", body)
	}
}

func TestDeepSeekChatBodyPreservesMultimodalContent(t *testing.T) {
	client := NewOpenAICompatibleClient("deepseek", "https://api.deepseek.com", "token", 0)
	message := Message{
		Role: "user",
		ContentParts: []ContentPart{
			TextPart("describe this image"),
			ImageURLPart("data:image/png;base64,AAAA"),
		},
	}
	body := client.chatBody(Request{
		Model:    "deepseek-v4-flash-vision-exp",
		Messages: []Message{message},
	})

	messages, ok := body["messages"].([]Message)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want one Message", body["messages"])
	}
	payload, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	bodyText := string(payload)
	for _, want := range []string{
		`"type":"text"`,
		`"type":"image_url"`,
		"describe this image",
		"data:image/png;base64,AAAA",
	} {
		if !strings.Contains(bodyText, want) {
			t.Fatalf("serialized message missing %q: %s", want, bodyText)
		}
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
		Temperature: float64Ptr(0),
	})

	if got := body["model"]; got != "deepseek-v4-flash" {
		t.Fatalf("model = %#v, want deepseek-v4-flash", got)
	}
	if got := body["max_tokens"]; got != 1234 {
		t.Fatalf("max_tokens = %#v, want 1234", got)
	}
	if got := body["temperature"]; got != float64(0) {
		t.Fatalf("temperature = %#v, want 0 when explicitly configured", got)
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
		OnText:             func(delta TextDelta) { streamed += delta.Text },
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

	client := NewOpenAICompatibleClient("test", server.URL, "token", 0)
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

func TestOpenAICompatibleChatStreamParsesDeltaToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"code-search\",\"arguments\":\"{\\\"query\\\":\\\"hel\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"code-search\",\"arguments\":\"lo\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient("test", server.URL, "token", 0)
	response, err := client.ChatStream(context.Background(), Request{Model: "test"}, StreamHandler{})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	calls := response.Message.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("ToolCalls = %#v, want one", calls)
	}
	if call := calls[0]; call.Function.Name != "code-search" || call.Function.Arguments != `{"query":"hello"}` {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestToolArgumentStream(t *testing.T) {
	tests := []struct {
		name    string
		updates []string
		want    string
	}{
		{name: "delta fragments", updates: []string{`{"query":"`, `hello"}`}, want: `{"query":"hello"}`},
		{name: "cumulative snapshots", updates: []string{`{"query":"hel`, `{"query":"hello"}`}, want: `{"query":"hello"}`},
		{name: "repeated cumulative snapshot", updates: []string{`{"query":"hel`, `{"query":"hel`, `{"query":"hello"}`}, want: `{"query":"hello"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stream toolArgumentStream
			stream.snapshotValid = true
			for _, update := range test.updates {
				stream.Append(update)
			}
			got := stream.Final()
			if got != test.want {
				t.Fatalf("Final() = %q, want %q", got, test.want)
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
