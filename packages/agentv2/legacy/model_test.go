package legacy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

type fakeStreamClient struct{ request llm.Request }

func (f *fakeStreamClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	panic("unexpected non-stream call")
}
func (f *fakeStreamClient) ChatStream(_ context.Context, request llm.Request, handler llm.StreamHandler) (llm.Response, error) {
	f.request = request
	handler.OnText("answer")
	handler.OnUsage(llm.Usage{PromptTokens: 7, CompletionTokens: 3})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "answer"}, FinishReason: "stop", Usage: llm.Usage{PromptTokens: 7, CompletionTokens: 3}}, nil
}

func TestModelAdaptsCanonicalRequestAndStream(t *testing.T) {
	client := &fakeStreamClient{}
	var events []model.StreamEvent
	response, err := (Model{Client: client}).Generate(context.Background(), model.Request{
		Model: "controlled", Messages: []model.Message{model.TextMessage(model.RoleUser, "question")},
		Tools: []model.ToolDefinition{{Name: "read", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, MaxOutputTokens: 99,
	}, func(event model.StreamEvent) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Message.Text(); got != "answer" {
		t.Fatalf("response text = %q", got)
	}
	if len(events) != 2 || events[0].Type != model.StreamTextDelta || events[0].Text != "answer" {
		t.Fatalf("events = %#v", events)
	}
	if client.request.Model != "controlled" || client.request.MaxTokens != 99 || len(client.request.Tools) != 1 {
		t.Fatalf("legacy request = %#v", client.request)
	}
}
