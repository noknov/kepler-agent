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

func TestTerminalReceiptsDoNotCompleteResidualToolCalls(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		for _, arguments := range []string{`{"path":"/tmp/file"}`, `{"path":`} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", reason, arguments, streaming), func(t *testing.T) {
					call := map[string]any{"id": "call", "type": "function", "function": map[string]string{"name": "write", "arguments": arguments}}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						choice := map[string]any{"finish_reason": reason}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							call["index"] = 0
							choice["delta"] = map[string]any{"content": "partial answer", "tool_calls": []any{call}}
							data, _ := json.Marshal(map[string]any{"choices": []any{choice}, "usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 9}})
							fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
						} else {
							choice["message"] = map[string]any{"role": "assistant", "content": "partial answer", "tool_calls": []any{call}}
							_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{choice}, "usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 9}})
						}
					}))
					defer server.Close()
					client := &Client{Wire: llm.NewOpenAICompatibleClient("test", server.URL, "test-token", 0)}
					completed := 0
					var sink model.EventSink
					if streaming {
						sink = func(event model.StreamEvent) error {
							if event.Type == model.StreamToolCallDone {
								completed++
							}
							return nil
						}
					}
					response, err := client.Generate(t.Context(), model.Request{Model: "test"}, sink)
					want := model.FinishLength
					if reason == "content_filter" {
						want = model.FinishContent
					}
					if err != nil || response.FinishReason != want || len(response.Message.ToolCalls()) != 0 || completed != 0 || response.Usage.OutputTokens != 9 || response.Message.Text() != "partial answer" {
						t.Fatalf("terminal receipt lost or tool completed: response=%+v completed=%d err=%v", response, completed, err)
					}
				})
			}
		}
	}
}

func TestCompletedEventFailureIsReturnedWithUsage(t *testing.T) {
	expected := fmt.Errorf("event destination unavailable")
	client := &Client{Wire: streamingWire{response: llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}, FinishReason: "stop", Usage: llm.Usage{CompletionTokens: 9}}}}
	response, err := client.Generate(t.Context(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
		if event.Type == model.StreamCompleted {
			return expected
		}
		return nil
	})
	if err != expected || response.Usage.OutputTokens != 9 {
		t.Fatalf("completion error lost: response=%+v err=%v", response, err)
	}
}

func TestInvalidFinalToolBatchPublishesNoCompletedCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"write","arguments":"{}"}},{"index":1,"id":"second","function":{"name":"write","arguments":"{"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":9}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := &Client{Wire: llm.NewOpenAICompatibleClient("test", server.URL, "test-token", 0)}
	completed := 0
	response, err := client.Generate(t.Context(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
		if event.Type == model.StreamToolCallDone {
			completed++
		}
		return nil
	})
	if err == nil || completed != 0 || response.Usage.OutputTokens != 9 {
		t.Fatalf("invalid batch completed %d calls, usage=%+v: %v", completed, response.Usage, err)
	}
}
