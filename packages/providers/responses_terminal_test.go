package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
)

func TestResponsesPreservesTerminalReasonAndUsage(t *testing.T) {
	for _, test := range []struct {
		name, status, reason string
		output               []any
		want                 model.FinishReason
	}{
		{"empty completed", "completed", "", nil, model.FinishStop},
		{"reasoning limit", "incomplete", "max_output_tokens", nil, model.FinishLength},
		{"message limit", "incomplete", "max_messages", nil, model.FinishLength},
		{"content filter", "incomplete", "content_filter", nil, model.FinishContent},
		{"refusal", "completed", "", []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "refusal", "refusal": "refused"}}}}, model.FinishContent},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", test.name, streaming), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					response := map[string]any{"status": test.status, "output": test.output, "usage": map[string]int{"input_tokens": 4, "output_tokens": 9}}
					if test.reason != "" {
						response["incomplete_details"] = map[string]string{"reason": test.reason}
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						data, _ := json.Marshal(map[string]any{"type": "response." + test.status, "response": response})
						fmt.Fprintf(w, "data: %s\n\n", data)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(response)
					}
				}))
				defer server.Close()
				client := &Client{Wire: llm.NewOpenAIResponsesClient("test", server.URL, "", 0)}
				var sink model.EventSink
				if streaming {
					sink = func(model.StreamEvent) error { return nil }
				}
				response, err := client.Generate(t.Context(), model.Request{Model: "test"}, sink)
				if err != nil || response.FinishReason != test.want || response.Usage.InputTokens != 4 || response.Usage.OutputTokens != 9 || len(response.Message.ToolCalls()) != 0 {
					t.Fatalf("response=%+v err=%v, want=%s", response, err, test.want)
				}
			})
		}
	}
}

func TestResponsesEOFDoesNotCommitProvisionalCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc\",\"call_id\":\"call\",\"name\":\"write\",\"arguments\":\"{}\"}}\n\n")
	}))
	defer server.Close()
	client := &Client{Wire: llm.NewOpenAIResponsesClient("test", server.URL, "", 0)}
	completed := 0
	response, err := client.Generate(t.Context(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
		if event.Type == model.StreamToolCallDone {
			completed++
		}
		return nil
	})
	if err == nil || completed != 0 || len(response.Message.ToolCalls()) != 0 {
		t.Fatalf("provisional call committed: response=%+v completed=%d err=%v", response, completed, err)
	}
}
