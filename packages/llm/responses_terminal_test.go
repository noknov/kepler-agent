package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesToolCompletionUsesFinalSnapshot(t *testing.T) {
	for _, test := range []struct {
		status, reason string
		wantCalls      int
	}{
		{"completed", "", 1},
		{"incomplete", "max_output_tokens", 0},
		{"incomplete", "content_filter", 0},
		{"incomplete", "steered", 0},
		{"failed", "", 0},
	} {
		t.Run(test.status+"/"+test.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"provisional\",\"type\":\"function_call\",\"call_id\":\"old\",\"name\":\"old\",\"arguments\":\"{}\"}}\n\n")
				response := map[string]any{
					"status": test.status, "incomplete_details": map[string]string{"reason": test.reason},
					"output": []any{map[string]string{"id": "final", "type": "function_call", "call_id": "final", "name": "lookup", "arguments": "{}"}},
					"usage":  map[string]int{"input_tokens": 4, "output_tokens": 9},
				}
				data, _ := json.Marshal(map[string]any{"type": "response." + test.status, "response": response})
				fmt.Fprintf(w, "data: %s\n\n", data)
			}))
			defer server.Close()
			var calls []ToolCall
			client := NewOpenAIResponsesClient("test", server.URL, "", 0)
			response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{OnToolCallComplete: func(call ToolCall) { calls = append(calls, call) }})
			if (err != nil) != (test.status == "failed" || test.reason == "steered") || len(calls) != test.wantCalls || len(response.Message.ToolCalls) != test.wantCalls || response.Usage.CompletionTokens != 9 {
				t.Fatalf("response=%+v callbacks=%+v err=%v", response, calls, err)
			}
			if test.wantCalls == 1 && (calls[0].ID != "final" || response.Message.ToolCalls[0].ID != "final") {
				t.Fatalf("provisional tool identity leaked: %+v %+v", response.Message.ToolCalls, calls)
			}
		})
	}
}

type responseTailError struct{}

func (responseTailError) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read beyond terminal receipt")
}

func TestResponsesStopsReadingAtTerminalReceipt(t *testing.T) {
	client := NewOpenAIResponsesClient("test", "https://provider.example", "", 0)
	client.SetTransport(qoderTestTransport(func(r *http.Request) (*http.Response, error) {
		body := io.MultiReader(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n"), responseTailError{})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	}))
	response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{})
	if err != nil || response.Message.Content != "done" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
