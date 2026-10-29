package llm

import (
	"encoding/json"
	"errors"
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

func TestResponsesRetainsCompletedItemsWithSparseTerminalReceipt(t *testing.T) {
	for _, provider := range []string{"chatgpt", "openai"} {
		t.Run(provider, func(t *testing.T) {
			client := NewOpenAIResponsesClient(provider, "https://provider.example", "", 0)
			client.SetTransport(qoderTestTransport(func(*http.Request) (*http.Response, error) {
				// The official ChatGPT route sends the full item in item.done,
				// then completes with output:[] and the token receipt.
				data := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"phase\":\"final_answer\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":5}}}\n\n"
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}, nil
			}))
			response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{})
			if err != nil || response.Message.Content != "OK" || len(response.Message.NativeItems) != 1 || !response.Usage.Reported || response.Usage.CompletionTokens != 5 {
				t.Fatalf("completed output lost: response=%+v err=%v", response, err)
			}
		})
	}
}

func TestResponsesSparseCallsWaitForSuccessfulTerminal(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "eof"} {
		t.Run(status, func(t *testing.T) {
			client := NewOpenAIResponsesClient("chatgpt", "https://provider.example", "", 0)
			client.SetTransport(qoderTestTransport(func(*http.Request) (*http.Response, error) {
				data := "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc\",\"type\":\"function_call\",\"call_id\":\"call\",\"name\":\"echo\",\"arguments\":\"{}\"}}\n\n" +
					"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"reason\",\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"}}\n\n"
				if status != "eof" {
					data += fmt.Sprintf("data: {\"type\":\"response.%s\",\"response\":{\"status\":%q,\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"error\":{\"code\":\"server_error\"},\"usage\":{\"input_tokens\":4,\"output_tokens\":9}}}\n\n", status, status)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}, nil
			}))
			calls := 0
			response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{OnToolCallComplete: func(ToolCall) { calls++ }})
			if status == "completed" {
				if err != nil || calls != 1 || len(response.Message.ToolCalls) != 1 || len(response.Message.NativeItems) != 2 || !strings.Contains(string(response.Message.NativeItems[0]), `"encrypted_content":"opaque"`) || response.Message.ToolCalls[0].ID != "call" {
					t.Fatalf("lost ordered completed items: response=%+v calls=%d err=%v", response, calls, err)
				}
			} else if calls != 0 || len(response.Message.ToolCalls) != 0 || len(response.Message.NativeItems) != 0 || (err != nil) != (status == "failed" || status == "eof") {
				t.Fatalf("unsafe terminal: response=%+v calls=%d err=%v", response, calls, err)
			}
			if status != "eof" && response.Usage.CompletionTokens != 9 {
				t.Fatalf("lost usage: %+v", response.Usage)
			}
		})
	}
}

func TestResponsesSparseOutputRejectsAmbiguousIndexes(t *testing.T) {
	for _, indexes := range []string{`{}`, `{"output_index":-1}`, `{"output_index":1024}`, `{"output_index":1}`, `{"output_index":0},{"output_index":0}`} {
		t.Run(indexes, func(t *testing.T) {
			client := NewOpenAIResponsesClient("chatgpt", "https://provider.example", "", 0)
			client.SetTransport(qoderTestTransport(func(*http.Request) (*http.Response, error) {
				var envelopes []map[string]any
				if err := json.Unmarshal([]byte("["+indexes+"]"), &envelopes); err != nil {
					t.Fatal(err)
				}
				var data strings.Builder
				for _, e := range envelopes {
					e["type"] = "response.output_item.done"
					e["item"] = map[string]string{"id": "fc", "type": "function_call", "call_id": "call", "name": "echo", "arguments": "{}"}
					raw, _ := json.Marshal(e)
					fmt.Fprintf(&data, "data: %s\n\n", raw)
				}
				data.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":9}}}\n\n")
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data.String()))}, nil
			}))
			calls := 0
			response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{OnToolCallComplete: func(ToolCall) { calls++ }})
			var protocol ProtocolError
			if !errors.As(err, &protocol) || calls != 0 || len(response.Message.ToolCalls) != 0 || response.Usage.CompletionTokens != 9 {
				t.Fatalf("ambiguous output accepted: response=%+v calls=%d err=%v", response, calls, err)
			}
		})
	}
}

func TestResponsesCompletedOutputHasAnAggregateByteBudget(t *testing.T) {
	var output responsesStreamOutput
	item, _ := json.Marshal(map[string]any{"id": "message", "type": "message", "content": []any{map[string]string{"type": "output_text", "text": strings.Repeat("x", 900000)}}})
	for index := 0; index < 10; index++ {
		output.add(&index, item)
	}
	terminal := responsesResponse{Status: "completed"}
	var protocol ProtocolError
	if err := output.populate(&terminal); !errors.As(err, &protocol) || len(terminal.Output) != 0 || len(terminal.RawOutput) != 0 {
		t.Fatalf("over-budget output was exposed: items=%d err=%v", len(terminal.Output), err)
	}
}
