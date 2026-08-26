package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
)

type recordingWire struct {
	request  llm.Request
	response llm.Response
}

type streamingWire struct{ response llm.Response }

func (w streamingWire) Chat(context.Context, llm.Request) (llm.Response, error) {
	return w.response, nil
}

func (w streamingWire) ChatStream(_ context.Context, _ llm.Request, handler llm.StreamHandler) (llm.Response, error) {
	if handler.OnText != nil {
		handler.OnText(llm.TextDelta{Text: "Let me inspect this."})
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

func TestClientDoesNotStreamTextFromToolCallStep(t *testing.T) {
	client := &Client{Wire: streamingWire{response: llm.Response{Message: llm.Message{Role: "assistant", Content: "Let me inspect this.", ToolCalls: []llm.ToolCall{{ID: "call", Type: "function", Function: llm.ToolFunction{Name: "read", Arguments: `{}`}}}}, FinishReason: "tool_calls"}}}
	var text string
	response, err := client.Generate(context.Background(), model.Request{Model: "test"}, func(event model.StreamEvent) error {
		if event.Type == model.StreamTextDelta {
			text += event.Text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
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
