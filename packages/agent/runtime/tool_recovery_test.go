package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestToolCanRecoverAfterRepeatedIdenticalNegativeResults(t *testing.T) {
	calls := 0
	item := auditTool{name: "run_tests", execute: func(context.Context) (tool.Result, error) {
		calls++
		result := tool.TextResult("test diagnostics")
		result.IsError = calls < 5
		if result.IsError {
			result.ErrorCode = "exit_1"
		}
		return result, nil
	}}
	catalog, _ := tool.NewCatalog(item)
	client := &scriptedModel{}
	for i := 0; i < 5; i++ {
		call := model.ToolCall{ID: fmt.Sprintf("test-%d", i), Name: "run_tests", Arguments: json.RawMessage(`{}`)}
		client.responses = append(client.responses, model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls})
	}
	client.responses = append(client.responses, model.Response{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop})
	runner, err := New(Config{MaxSteps: 8}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "fix tests iteratively")}); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("legitimate diagnostic reruns were blocked after %d calls", calls)
	}
}
