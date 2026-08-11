package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type recordingWire struct{ request llm.Request }

func (w *recordingWire) Chat(_ context.Context, request llm.Request) (llm.Response, error) {
	w.request = request
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}, nil
}

type roundTripEcho struct{}

func (roundTripEcho) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (roundTripEcho) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.TextResult("echoed"), nil
}

func TestClientConvertsCanonicalRequestOnceForEveryProfile(t *testing.T) {
	wire := &recordingWire{}
	client := &Client{Wire: wire}
	response, err := client.Generate(context.Background(), model.Request{
		Model: "test", Temperature: 0.25, MaxOutputTokens: 123,
		Messages: []model.Message{model.TextMessage(model.RoleUser, "hello")},
		Tools:    []model.ToolDefinition{{Name: "echo", InputSchema: []byte(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Text() != "ok" || wire.request.Model != "test" || wire.request.Temperature != 0.25 || wire.request.MaxTokens != 123 {
		t.Fatalf("response=%+v request=%+v", response, wire.request)
	}
	if len(wire.request.Tools) != 1 || wire.request.Tools[0].Function.Name != "echo" {
		t.Fatalf("tools=%+v", wire.request.Tools)
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

func TestResponsesFactoryCompletesCanonicalToolRoundTrip(t *testing.T) {
	requests := 0
	var secondInput []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_internal\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"fc_internal\",\"type\":\"function_call\",\"name\":\"echo\",\"call_id\":\"call_public\",\"arguments\":\"{}\"}]}}\n\n")
			return
		}
		secondInput, _ = body["input"].([]any)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
	}))
	defer server.Close()
	client, err := New(Config{Provider: "openai", Protocol: "responses", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := tool.NewCatalog(roundTripEcho{})
	runner, _ := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	result, err := runner.RunTurn(context.Background(), agentruntime.TurnRequest{SessionID: "responses", Input: model.TextMessage(model.RoleUser, "echo")})
	if err != nil || result.Message.Text() != "done" || requests != 2 {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
	}
	var sawCall, sawOutput bool
	for _, raw := range secondInput {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call" && item["call_id"] == "call_public" {
			sawCall = true
		}
		if item["type"] == "function_call_output" && item["call_id"] == "call_public" {
			sawOutput = true
		}
	}
	if !sawCall || !sawOutput {
		t.Fatalf("second request input=%#v", secondInput)
	}
}
