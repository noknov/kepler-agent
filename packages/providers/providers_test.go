package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
)

type recordingWire struct {
	request  llm.Request
	response llm.Response
}

type streamingWire struct{ response llm.Response }

type deltaStreamingWire struct {
	deltas   []llm.TextDelta
	response llm.Response
}

func (w streamingWire) Chat(context.Context, llm.Request) (llm.Response, error) {
	return w.response, nil
}

func (w streamingWire) ChatStream(_ context.Context, _ llm.Request, handler llm.StreamHandler) (llm.Response, error) {
	if handler.OnText != nil {
		handler.OnText(llm.TextDelta{Text: "Let me inspect this."})
	}
	return w.response, nil
}

func (w deltaStreamingWire) Chat(context.Context, llm.Request) (llm.Response, error) {
	return w.response, nil
}

func (w deltaStreamingWire) ChatStream(_ context.Context, _ llm.Request, handler llm.StreamHandler) (llm.Response, error) {
	for _, delta := range w.deltas {
		if handler.OnText != nil {
			handler.OnText(delta)
		}
	}
	return w.response, nil
}

type dualModeWire struct {
	chatCalls   int
	streamCalls int
}

func (w *dualModeWire) Chat(context.Context, llm.Request) (llm.Response, error) {
	w.chatCalls++
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "complete"}, FinishReason: "stop"}, nil
}

func (w *dualModeWire) ChatStream(_ context.Context, _ llm.Request, _ llm.StreamHandler) (llm.Response, error) {
	w.streamCalls++
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "streamed"}, FinishReason: "stop"}, nil
}

func TestClientUsesCompletionWithoutEventSink(t *testing.T) {
	wire := &dualModeWire{}
	response, err := (&Client{Wire: wire}).Generate(context.Background(), model.Request{Model: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Text() != "complete" || wire.chatCalls != 1 || wire.streamCalls != 0 {
		t.Fatalf("response=%q chat=%d stream=%d", response.Message.Text(), wire.chatCalls, wire.streamCalls)
	}
}

func TestClientUsesStreamingWithEventSink(t *testing.T) {
	wire := &dualModeWire{}
	_, err := (&Client{Wire: wire}).Generate(context.Background(), model.Request{Model: "test"}, func(model.StreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if wire.chatCalls != 0 || wire.streamCalls != 1 {
		t.Fatalf("chat=%d stream=%d", wire.chatCalls, wire.streamCalls)
	}
}

func TestClientPublishesUnphasedTextDeltasInOrder(t *testing.T) {
	client := &Client{Wire: deltaStreamingWire{
		deltas:   []llm.TextDelta{{Text: "fast "}, {Text: "stream"}},
		response: llm.Response{Message: llm.Message{Role: "assistant", Content: "fast stream"}, FinishReason: "stop"},
	}}
	var received []string
	_, err := client.Generate(context.Background(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
		if event.Type == model.StreamTextDelta {
			received = append(received, event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(received, ""); got != "fast stream" {
		t.Fatalf("streamed text = %q, want fast stream", got)
	}
	if len(received) != 2 {
		t.Fatalf("delta count = %d, want 2", len(received))
	}
}

func TestClientDoesNotPublishReasoningDeltas(t *testing.T) {
	client := &Client{Wire: deltaStreamingWire{
		deltas: []llm.TextDelta{
			{Text: "private", Phase: "reasoning"},
			{Text: "visible", Phase: "final_answer"},
		},
		response: llm.Response{Message: llm.Message{Role: "assistant", Content: "visible"}, FinishReason: "stop"},
	}}
	var received []string
	_, err := client.Generate(context.Background(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
		if event.Type == model.StreamTextDelta {
			received = append(received, event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(received, ""); got != "visible" {
		t.Fatalf("streamed text = %q, want visible", got)
	}
}

func (w *recordingWire) Chat(_ context.Context, request llm.Request) (llm.Response, error) {
	w.request = request
	if w.response.FinishReason != "" || w.response.Message.Content != "" {
		return w.response, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}, nil
}

func TestFinishReasonMapsNetworkError(t *testing.T) {
	reason := finishReason("network_error")
	if reason != model.FinishError {
		t.Fatalf("finishReason() = %q, want error", reason)
	}
}

func TestFinishReasonTreatsUnknownProviderFailureAsError(t *testing.T) {
	if got := finishReason("upstream_unavailable"); got != model.FinishError {
		t.Fatalf("finishReason() = %q, want error", got)
	}
}

func TestClientMapsProviderFinishErrorToRetryableModelError(t *testing.T) {
	wire := &recordingWire{
		response: llm.Response{
			Message:      llm.Message{Role: "assistant", Content: "partial"},
			FinishReason: "network_error",
		},
	}
	client := &Client{Wire: wire}
	_, err := client.Generate(context.Background(), model.Request{
		Model:    "test",
		Messages: []model.Message{model.TextMessage(model.RoleUser, "hi")},
	}, nil)
	if err == nil {
		t.Fatal("expected retryable model error")
	}
	var typed *model.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T, want *model.Error", err)
	}
	if !typed.Retryable || typed.Kind != model.ErrorTransient {
		t.Fatalf("model error = retryable=%v kind=%q, want transient retryable", typed.Retryable, typed.Kind)
	}
}

func TestToToolCallClassifiesMalformedArgumentsAsRetryableProtocolError(t *testing.T) {
	_, err := toToolCall(llm.ToolCall{ID: "call-1", Function: llm.ToolFunction{Name: "tool_search", Arguments: `{"action":"search"`}})
	if err == nil {
		t.Fatal("expected malformed arguments error")
	}
	var typed *model.Error
	if !errors.As(err, &typed) || typed.Kind != model.ErrorProtocol || !typed.Retryable {
		t.Fatalf("error = %#v, want retryable protocol error", err)
	}
	if strings.Contains(err.Error(), `{"action"`) || !strings.Contains(err.Error(), "bytes=") || !strings.Contains(err.Error(), "sha256_prefix=") {
		t.Fatalf("error must contain safe diagnostics without raw arguments: %q", err)
	}
}

func TestClientConvertsCanonicalRequestOnceForEveryProfile(t *testing.T) {
	wire := &recordingWire{}
	client := &Client{Wire: wire}
	temp := 0.25
	response, err := client.Generate(context.Background(), model.Request{
		Model: "test", Temperature: &temp, MaxOutputTokens: 123,
		Messages: []model.Message{model.TextMessage(model.RoleUser, "hello")},
		Tools:    []model.ToolDefinition{{Name: "echo", InputSchema: []byte(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Text() != "ok" || wire.request.Model != "test" || wire.request.Temperature == nil || *wire.request.Temperature != 0.25 || wire.request.MaxTokens != 123 {
		t.Fatalf("response=%+v request=%+v", response, wire.request)
	}
	if len(wire.request.Tools) != 1 || wire.request.Tools[0].Function.Name != "echo" {
		t.Fatalf("tools=%+v", wire.request.Tools)
	}
}

func TestClientNormalizesSchemaBeforeWireEncoding(t *testing.T) {
	wire := &recordingWire{}
	client := &Client{Wire: wire}
	_, err := client.Generate(context.Background(), model.Request{
		Model: "test",
		Tools: []model.ToolDefinition{{
			Name:        "remote-empty-tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":null,"items":{"type":"object","properties":null},"examples":[{"properties":null}]}`),
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.request.Tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", wire.request.Tools)
	}
	encoded, err := json.Marshal(wire.request.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["properties"] == nil {
		t.Fatalf("root properties = null, want empty object: %s", encoded)
	}
	items := schema["items"].(map[string]any)
	if items["properties"] == nil {
		t.Fatalf("nested properties = null, want empty object: %s", encoded)
	}
	examples := schema["examples"].([]any)
	example := examples[0].(map[string]any)
	if _, exists := example["properties"]; !exists || example["properties"] != nil {
		t.Fatalf("example data was modified: %#v", example)
	}
}

func TestNormalizeJSONSchemaTraversesOnlySchemaKeywords(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{
		"$defs":{"named":{"type":"object","properties":null}},
		"allOf":[{"type":"object","properties":null}],
		"dependencies":{"schema":{"type":"object","properties":null},"names":["enabled"]},
		"default":{"properties":null},
		"x-ui":{"properties":null}
	}`), &schema); err != nil {
		t.Fatal(err)
	}

	normalizeJSONSchema(schema)

	if got := schema["$defs"].(map[string]any)["named"].(map[string]any)["properties"]; got == nil {
		t.Fatal("$defs schema properties = null, want empty object")
	}
	if got := schema["allOf"].([]any)[0].(map[string]any)["properties"]; got == nil {
		t.Fatal("allOf schema properties = null, want empty object")
	}
	dependencies := schema["dependencies"].(map[string]any)
	if got := dependencies["schema"].(map[string]any)["properties"]; got == nil {
		t.Fatal("dependency schema properties = null, want empty object")
	}
	if got := dependencies["names"].([]any); len(got) != 1 || got[0] != "enabled" {
		t.Fatalf("dependency names were modified: %#v", got)
	}
	if got := schema["default"].(map[string]any)["properties"]; got != nil {
		t.Fatalf("default was modified: %#v", schema["default"])
	}
	if got := schema["x-ui"].(map[string]any)["properties"]; got != nil {
		t.Fatalf("vendor metadata was modified: %#v", schema["x-ui"])
	}
}

func TestClientPreservesTextInMultimodalWireMessage(t *testing.T) {
	wire := &recordingWire{}
	client := &Client{Wire: wire}
	_, err := client.Generate(context.Background(), model.Request{
		Model: "test",
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Content{
			{Type: model.ContentText, Text: "explain this image"},
			{Type: model.ContentImage, ImageURL: "data:image/png;base64,AAAA"},
		}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	message := wire.request.Messages[0]
	if message.Content != "" || len(message.ContentParts) != 2 || message.ContentParts[0].Text != "explain this image" {
		t.Fatalf("wire message = %+v", message)
	}
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "explain this image") {
		t.Fatalf("serialized message lost text: %s", payload)
	}
}

func TestClientRoutesToolStepTextToCommentary(t *testing.T) {
	client := &Client{Wire: streamingWire{response: llm.Response{Message: llm.Message{Role: "assistant", Content: "Let me inspect this.", ToolCalls: []llm.ToolCall{{ID: "call", Type: "function", Function: llm.ToolFunction{Name: "read", Arguments: `{}`}}}}, FinishReason: "tool_calls"}}}
	var text, commentary string
	response, err := client.Generate(context.Background(), model.Request{Model: "test", Tools: []model.ToolDefinition{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}, func(event model.StreamEvent) error {
		if event.Type == model.StreamTextDelta {
			text += event.Text
		}
		if event.Type == model.StreamCommentary {
			commentary += event.Text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" || commentary != "Let me inspect this." {
		t.Fatalf("streamed text = %q", text)
	}
	if got := response.Message.Text(); got != "" {
		t.Fatalf("durable text = %q", got)
	}
}

func TestFactoryRejectsUnknownProtocol(t *testing.T) {
	if _, err := New(Config{Provider: "custom", Protocol: "unknown"}); err == nil {
		t.Fatal("expected unsupported protocol error")
	}
}

func TestFactoryResponsesPreservesCallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_internal\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"fc_internal\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}]}}\n\n")
	}))
	defer server.Close()
	client, err := New(Config{Provider: "openai", Protocol: "responses", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), model.Request{Model: "test", Messages: []model.Message{model.TextMessage(model.RoleUser, "call")}}, func(model.StreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	calls := response.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_public" {
		t.Fatalf("tool calls = %+v, want preserved public call ID", calls)
	}
}

func TestQoderFactoryPreservesCanonicalAdapter(t *testing.T) {
	c, err := New(Config{Provider: "qoder", Protocol: "qoder", BaseURL: llm.QoderBaseURL, APIKey: "synthetic-pat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Wire.(*llm.QoderClient); !ok || c.Host != nil {
		t.Fatal("Qoder bypassed the canonical provider adapter")
	}
	if _, err := New(Config{Provider: "qoder", Protocol: "openai", BaseURL: llm.QoderBaseURL, APIKey: "synthetic-pat"}); err == nil {
		t.Fatal("accepted wrong Qoder protocol")
	}
}

func TestOtherStreamingProtocolsStillPublishBeforeCompletion(t *testing.T) {
	cases := []struct{ provider, protocol, first, last string }{
		{"deepseek", "openai", `data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n", `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"},
		{"mimo", "anthropic", `event: message_start
data: {"type":"message_start","message":{"role":"assistant","content":[]}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

`, `event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}

`},
		{"openai", "responses", `data: {"type":"response.output_item.added","item":{"type":"message","id":"final","role":"assistant","phase":"final_answer","content":[]}}

data: {"type":"response.output_text.delta","item_id":"final","delta":"hello"}

`, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"final","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"hello"}]}]}}

`},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.protocol, func(t *testing.T) {
			arrived := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.first)
				w.(http.Flusher).Flush()
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					t.Error("provider no longer presents deltas before stream completion")
				}
				_, _ = fmt.Fprint(w, tc.last)
			}))
			defer server.Close()
			client, err := New(Config{Provider: tc.provider, Protocol: tc.protocol, BaseURL: server.URL, APIKey: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			var text string
			result, err := client.Generate(context.Background(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
				if event.Type == model.StreamTextDelta {
					text += event.Text
					once.Do(func() { close(arrived) })
				}
				return nil
			})
			if err != nil || text != "hello" || result.Message.Text() != "hello" {
				t.Fatalf("streaming regressed: text=%q result=%q err=%v", text, result.Message.Text(), err)
			}
		})
	}
}

func TestToolEnabledFinalAnswerIsNotFilteredByItsWording(t *testing.T) {
	const answer = "Let me inspect this. 我来分析一下。"
	client := &Client{Wire: deltaStreamingWire{deltas: []llm.TextDelta{{Text: answer}}, response: llm.Response{Message: llm.Message{Role: "assistant", Content: answer}, FinishReason: "stop"}}}
	var final, commentary string
	response, err := client.Generate(context.Background(), model.Request{Model: "test", Tools: []model.ToolDefinition{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}, func(event model.StreamEvent) error {
		if event.Type == model.StreamTextDelta {
			final += event.Text
		}
		if event.Type == model.StreamCommentary {
			commentary += event.Text
		}
		return nil
	})
	if err != nil || final != answer || response.Message.Text() != answer || commentary != "" {
		t.Fatalf("wording-based filtering: final=%q commentary=%q err=%v", final, commentary, err)
	}
}

type nativeQoderTransport func(*http.Request) (*http.Response, error)

func (f nativeQoderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nativeQoderEventReader struct {
	chunks     []string
	index      int
	beforeRead func(int)
}

func (r *nativeQoderEventReader) Read(p []byte) (int, error) {
	if r.beforeRead != nil {
		r.beforeRead(r.index)
	}
	if r.index == len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	n := copy(p, chunk)
	if n == len(chunk) {
		r.index++
	} else {
		r.chunks[r.index] = chunk[n:]
	}
	return n, nil
}
func nativeQoderEvent(body string) string {
	envelope, _ := json.Marshal(map[string]any{"statusCodeValue": 200, "body": body})
	return "data: " + string(envelope) + "\n\n"
}
func nativeQoderClient(t *testing.T, body io.Reader) *Client {
	t.Helper()
	client, err := New(Config{Provider: "qoder", Protocol: "qoder", BaseURL: llm.QoderBaseURL, APIKey: "synthetic-pat"})
	if err != nil {
		t.Fatal(err)
	}
	client.Wire.(*llm.QoderClient).SetTransport(nativeQoderTransport(func(r *http.Request) (*http.Response, error) {
		var response io.Reader
		switch r.URL.Path {
		case "/api/v1/jobToken/exchange":
			response = strings.NewReader(fmt.Sprintf(`{"token":"synthetic-job","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339)))
		case "/api/v1/userinfo":
			response = strings.NewReader(`{"id":"synthetic-user"}`)
		case "/algo/api/v2/model/list":
			response = strings.NewReader(`{"assistant":[{"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true,"source":"system"}]}`)
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			response = body
		default:
			t.Fatalf("unexpected native endpoint %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(response)}, nil
	}))
	return client
}
func nativeQoderRequest() model.Request {
	return model.Request{Model: "Qwen3.8-Flash", Tools: []model.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
}
func TestNativeQoderClassifiesCompleteResponseWithoutTextHeuristics(t *testing.T) {
	const prose = "我来分析一下。A final answer may use exactly these same words."
	textChunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": prose}}}})
	for _, withTool := range []bool{true, false} {
		t.Run(fmt.Sprint("tool=", withTool), func(t *testing.T) {
			terminal := `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
			if withTool {
				terminal = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
			}
			var final, commentary string
			tokens := 0
			reader := &nativeQoderEventReader{chunks: []string{nativeQoderEvent(string(textChunk)), nativeQoderEvent(terminal), nativeQoderEvent("[DONE]")}}
			reader.beforeRead = func(index int) {
				if index > 0 && tokens != 1 {
					t.Error("first token was delayed until response completion")
				}
				if final != "" || commentary != "" {
					t.Error("unclassified text escaped before native response completion")
				}
			}
			response, err := nativeQoderClient(t, reader).Generate(context.Background(), nativeQoderRequest(), func(event model.StreamEvent) error {
				switch event.Type {
				case model.StreamFirstToken:
					tokens++
				case model.StreamTextDelta:
					final += event.Text
				case model.StreamCommentary:
					commentary += event.Text
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if withTool {
				if final != "" || response.Message.Text() != "" || commentary != prose || len(response.Message.ToolCalls()) != 1 {
					t.Fatalf("tool step misclassified: final=%q commentary=%q response=%+v", final, commentary, response)
				}
			} else {
				if final != prose || response.Message.Text() != prose || commentary != "" {
					t.Fatalf("wording filtered final answer: final=%q commentary=%q", final, commentary)
				}
			}
		})
	}
}
func TestNativeQoderDropsUnclassifiedTextOnFailure(t *testing.T) {
	start := nativeQoderEvent(`{"choices":[{"delta":{"content":"provisional text"}}]}`)
	for name, end := range map[string]string{"truncated": "", "malformed": "data: broken\n\n", "upstream error": "data: {\"statusCodeValue\":503,\"body\":\"error\"}\n\n"} {
		t.Run(name, func(t *testing.T) {
			presented := 0
			_, err := nativeQoderClient(t, strings.NewReader(start+end)).Generate(context.Background(), nativeQoderRequest(), func(event model.StreamEvent) error {
				if event.Type == model.StreamTextDelta || event.Type == model.StreamCommentary {
					presented++
				}
				return nil
			})
			if err == nil || presented != 0 {
				t.Fatalf("failed response presented provisional text: count=%d err=%v", presented, err)
			}
		})
	}
}

func TestQoderProtocolAndEmptyErrorsAreRetryable(t *testing.T) {
	for _, err := range []error{llm.ProtocolError{Provider: "qoder", Detail: "incomplete tool call"}, llm.EmptyResponseError{Provider: "qoder", StopReason: "stop"}} {
		var typed *model.Error
		if !errors.As(toModelError(err), &typed) || typed.Kind != model.ErrorProtocol || !typed.Retryable {
			t.Fatalf("error not classified: %v", err)
		}
	}
	other := llm.EmptyResponseError{Provider: "anthropic"}
	var typed *model.Error
	if errors.As(toModelError(other), &typed) {
		t.Fatal("changed another provider's empty-response behavior")
	}
}
