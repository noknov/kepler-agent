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

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/llm"
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

func TestClientNormalizesSchemasForEveryWireProtocol(t *testing.T) {
	wire := &recordingWire{}
	client := &Client{Wire: wire}
	_, err := client.Generate(context.Background(), model.Request{
		Model: "test",
		Tools: []model.ToolDefinition{{
			Name:        "remote-empty-tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":null,"items":{"type":"object","properties":null}}`),
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
	if strings.Contains(string(encoded), `"properties":null`) {
		t.Fatalf("provider schema retained null properties: %s", encoded)
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
	response, err := client.Generate(context.Background(), model.Request{Model: "test", Messages: []model.Message{model.TextMessage(model.RoleUser, "call")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := response.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_public" {
		t.Fatalf("tool calls = %+v, want preserved public call ID", calls)
	}
}
