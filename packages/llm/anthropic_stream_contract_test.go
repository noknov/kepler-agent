package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicStreamRejectsUnboundBlocksAndIncompleteMessages(t *testing.T) {
	start := "event: content_block_start\ndata: " + `{"index":0,"content_block":{"type":"tool_use","id":"call","name":"read","input":{}}}` + "\n\n"
	stop := "event: content_block_stop\ndata: " + `{"index":0}` + "\n\n"
	done := "event: message_delta\ndata: " + `{"delta":{"stop_reason":"tool_use"}}` + "\n\nevent: message_stop\ndata: {}\n\n"
	for _, tc := range []struct{ name, frames string }{
		{"unbound delta", start + "event: content_block_delta\ndata: " + `{"index":99,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" + stop + done},
		{"invalid tool input", start + "event: content_block_delta\ndata: " + `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{broken"}}` + "\n\n" + stop + done},
		{"wrong stop index", start + "event: content_block_stop\ndata: " + `{"index":99}` + "\n\n" + done},
		{"missing delta index", start + "event: content_block_delta\ndata: " + `{"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" + stop + done},
		{"duplicate start", start + start + stop + done},
		{"duplicate stop", start + stop + stop + done},
		{"open block at message stop", start + done},
		{"missing message stop", start + stop},
		{"invalid event JSON", "event: content_block_start\ndata: {\n\n" + done},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tc.frames))
			}))
			defer server.Close()
			client := NewAnthropicClient(server.URL, "token", 0, "official")
			client.httpClient = server.Client()
			response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{})
			var protocol ProtocolError
			if !errors.As(err, &protocol) {
				t.Fatalf("ambiguous/incomplete stream accepted: %+v %v", response, err)
			}
			if strings.Contains(err.Error(), "partial_json") {
				t.Fatal("protocol error exposed payload")
			}
		})
	}
}

func TestAnthropicStreamKeepsBlockIdentityAndUnknownEventCompatibility(t *testing.T) {
	const frames = `event: content_block_start
data: {"index":0,"content_block":{"type":"tool_use","id":"first","name":"read","input":{}}}

event: content_block_start
data: {"index":1,"content_block":{"type":"text","text":"before"}}

event: content_block_start
data: {"index":2,"content_block":{"type":"tool_use","id":"second","name":"list","input":{}}}

event: content_block_delta
data: {"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"source.txt\"}"}}

event: content_block_stop
data: {"index":2}

event: content_block_delta
data: {"index":1,"delta":{"type":"text_delta","text":" after"}}

event: content_block_stop
data: {"index":0}

event: content_block_stop
data: {"index":1}

event: future_event
data: {"type":"future_event"}

event: content_block_start
data: {"index":3,"content_block":{"type":"future_block"}}

event: content_block_delta
data: {"index":3,"delta":{"type":"future_delta"}}

event: content_block_stop
data: {"index":3}

event: message_delta
data: {"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(frames))
	}))
	defer server.Close()
	client := NewAnthropicClient(server.URL, "token", 0, "official")
	client.httpClient = server.Client()
	text := ""
	completed := 0
	response, err := client.ChatStream(t.Context(), Request{Model: "test"}, StreamHandler{OnText: func(delta TextDelta) { text += delta.Text }, OnToolCallComplete: func(ToolCall) { completed++ }})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Content != "before after" || text != response.Message.Content || completed != 2 || len(response.Message.ToolCalls) != 2 {
		t.Fatalf("block evidence lost: %+v text=%q completed=%d", response, text, completed)
	}
	calls := response.Message.ToolCalls
	if calls[0].ID != "first" || calls[0].Function.Arguments != `{"path":"source.txt"}` || calls[1].ID != "second" || calls[1].Function.Arguments != "{}" {
		t.Fatalf("tool blocks rebound or reordered: %+v", calls)
	}
}
