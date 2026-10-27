package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamRejectsIncompleteOrAmbiguousToolEvidence(t *testing.T) {
	for _, tc := range []struct{ name, frames string }{
		{"text EOF", `{"choices":[{"delta":{"content":"partial"}}]}`},
		{"tool EOF", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"read","arguments":"{}"}}]}}]}`},
		{"done without finish", `{"choices":[{"delta":{"content":"partial"}}]}` + "\n[DONE]"},
		{"missing tool index", `{"choices":[{"delta":{"tool_calls":[{"id":"call","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{"over-budget tool index", `{"choices":[{"delta":{"tool_calls":[{"index":1024,"id":"call","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{"reused index", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"read","arguments":"{}"}}]}}]}` + "\n" + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"second","function":{"name":"write","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{"renamed call", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"read"}}]}}]}` + "\n" + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"write","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{"invalid JSON event", "{\n" + `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`},
		{"post-completion content", `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}` + "\n" + `{"choices":[{"delta":{"content":"extra"}}]}`},
		{"negative tool index", `{"choices":[{"delta":{"tool_calls":[{"index":-1,"id":"call","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if got := recover(); got != nil {
					t.Errorf("untrusted stream panicked: %v", got)
				}
			}()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range strings.Split(tc.frames, "\n") {
					_, _ = w.Write([]byte("data: " + frame + "\n\n"))
				}
			}))
			defer server.Close()
			client := NewOpenAICompatibleClient("test", server.URL, "token", 0)
			client.httpClient = server.Client()
			completed := 0
			_, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{OnToolCallComplete: func(ToolCall) { completed++ }})
			var protocol ProtocolError
			if !errors.As(err, &protocol) || completed != 0 {
				t.Fatalf("ambiguous evidence accepted: err=%v completed=%d", err, completed)
			}
		})
	}
}

func TestChatStreamPreservesExplicitCompletionEvidence(t *testing.T) {
	for _, tc := range []struct {
		reason, frame string
		tools         int
	}{
		{"length", `{"choices":[{"delta":{"content":"partial"},"finish_reason":"length"}]}`, 0},
		{"tool_calls", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, 1},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: " + tc.frame + "\n\n"))
			}))
			defer server.Close()
			client := NewOpenAICompatibleClient("test", server.URL, "token", 0)
			client.httpClient = server.Client()
			completed := 0
			response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{OnToolCallComplete: func(ToolCall) { completed++ }})
			if err != nil || response.FinishReason != tc.reason || len(response.Message.ToolCalls) != tc.tools || completed != tc.tools {
				t.Fatalf("explicit completion lost: %+v completed=%d err=%v", response, completed, err)
			}
		})
	}
}
