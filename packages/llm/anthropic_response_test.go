package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicChatParsesToolUseBlocks(t *testing.T) {
	raw := anthropicResponse{
		Content: []anthropicBlock{
			{Type: "text", Text: "checking"},
			{
				Type:  "tool_use",
				ID:    "toolu_abc",
				Name:  "code-search",
				Input: json.RawMessage(`{"query":"Startup"}`),
			},
		},
		StopReason: "tool_use",
	}
	message := Message{Role: "assistant", Content: raw.text()}
	for _, block := range raw.Content {
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
	if len(message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v", message.ToolCalls)
	}
	if message.ToolCalls[0].Function.Name != "code-search" {
		t.Fatalf("tool name = %q", message.ToolCalls[0].Function.Name)
	}
	if message.Content != "checking" {
		t.Fatalf("content = %q", message.Content)
	}
}

func TestAnthropicEmptyResponseErrorMetadata(t *testing.T) {
	raw := anthropicResponse{
		Content: []anthropicBlock{
			{Type: "thinking", Text: "hidden reasoning"},
		},
		StopReason: "end_turn",
	}
	err := EmptyResponseError{
		Provider:     "anthropic messages",
		StopReason:   raw.StopReason,
		ContentTypes: raw.contentTypes(),
	}

	if !IsEmptyResponse(err) {
		t.Fatal("expected EmptyResponseError to be recognized")
	}
	var emptyErr EmptyResponseError
	if !errors.As(err, &emptyErr) {
		t.Fatal("expected errors.As to find EmptyResponseError")
	}
	if got := emptyErr.ContentTypes; len(got) != 1 || got[0] != "thinking" {
		t.Fatalf("ContentTypes = %#v, want [thinking]", got)
	}
	if emptyErr.Error() != "anthropic messages returned no text or tool calls (stop_reason=end_turn)" {
		t.Fatalf("Error() = %q", emptyErr.Error())
	}
}

func TestAnthropicChatStreamParsesToolUseBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"code-search\",\"input\":{}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Startup\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer server.Close()

	client := NewAnthropicClient(server.URL, "token", 0, "official")
	client.httpClient = server.Client()

	var streamed string
	var toolStarted bool
	var usageEvents []Usage
	resp, err := client.ChatStream(context.Background(), Request{
		Model: "claude-test",
		Tools: []ToolSpec{{Type: "function", Function: ToolSpecFunction{
			Name:       "code-search",
			Parameters: map[string]any{"type": "object"},
		}}},
	}, StreamHandler{
		OnText:             func(delta string) { streamed += delta },
		OnToolCallsStarted: func() { toolStarted = true },
		OnUsage:            func(usage Usage) { usageEvents = append(usageEvents, usage) },
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
	if resp.FinishReason != "tool_use" {
		t.Fatalf("FinishReason = %q, want tool_use", resp.FinishReason)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v, want one", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "toolu_1" || call.Type != "function" || call.Function.Name != "code-search" || call.Function.Arguments != `{"query":"Startup"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Fatalf("usage total = %d, want 12", resp.Usage.TotalTokens)
	}
	if len(usageEvents) != 2 {
		t.Fatalf("usage events = %#v, want message_start and message_delta", usageEvents)
	}
	if usageEvents[0].PromptTokens != 5 || usageEvents[0].CompletionTokens != 0 {
		t.Fatalf("first usage event = %#v, want input-only", usageEvents[0])
	}
	if usageEvents[1].PromptTokens != 5 || usageEvents[1].CompletionTokens != 7 || usageEvents[1].TotalTokens != 12 {
		t.Fatalf("second usage event = %#v, want cumulative usage", usageEvents[1])
	}
}
