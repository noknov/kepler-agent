package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestToolFailureIsModelVisibleWithoutReplacingPartialEvidence(t *testing.T) {
	result := tool.TextResult("compiler produced partial diagnostics")
	result.IsError = true
	result.ErrorCode = "command_timeout"
	original := append([]model.Content(nil), result.Content...)
	catalog, _ := tool.NewCatalog(auditTool{name: "command", execute: func(context.Context) (tool.Result, error) { return result, nil }})
	call := model.ToolCall{ID: "call", Name: "command", Arguments: json.RawMessage(`{}`)}
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
	}}
	store := transcript.NewMemoryStore()
	runner, err := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "compile")}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range client.requests[1].Messages {
		for _, block := range message.Content {
			if block.ToolResult == nil || block.ToolResult.CallID != call.ID {
				continue
			}
			content := block.ToolResult.Content
			if len(content) != 2 || content[0].Type != model.ContentJSON || content[1].Text != original[0].Text {
				t.Fatalf("failure status or original evidence lost: %+v", content)
			}
			var status map[string]string
			if err := json.Unmarshal(content[0].JSON, &status); err != nil {
				t.Fatal(err)
			}
			if status["tool_status"] != "error" || status["error_code"] != "command_timeout" {
				t.Fatalf("failure is ambiguous for text-only protocols: %+v", status)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("tool result missing from the next inference")
	}
	events, err := store.Load(t.Context(), "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == transcript.ToolCallFailed && !reflect.DeepEqual(event.ToolResult.Content, original) {
			t.Fatal("derived status mutated canonical evidence")
		}
	}
}
