package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type schemaTool struct{ name string }

func (t schemaTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: t.name, Description: strings.Repeat("schema description ", 40), InputSchema: json.RawMessage(`{"type":"object"}`), Exposure: tool.ExposureDeferred, Effects: []tool.Effect{tool.EffectRead}}
}
func (schemaTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

func TestActivationBudgetRejectsBulkLoadAndAllowsRecovery(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	names := make([]string, 469)
	for i := range names {
		names[i] = fmt.Sprintf("api_%03d", i)
		if err := catalog.Register(schemaTool{names[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Register(tool.NewSearchTool(catalog)); err != nil {
		t.Fatal(err)
	}
	bulk, _ := json.Marshal(map[string]any{"action": "activate", "tool_names": names})
	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "bulk", Name: "tool_search", Arguments: bulk}}}}, FinishReason: model.FinishToolCalls},
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "narrow", Name: "tool_search", Arguments: json.RawMessage(`{"action":"activate","tool_names":["api_000"]}`)}}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
	}}
	store := transcript.NewMemoryStore()
	runner, err := New(Config{Model: "test", Context: ContextConfig{MaxTokens: 20000, OutputTokens: 1000}}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunTurn(context.Background(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "find an API")}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 3 || len(client.requests[1].Tools) != 1 || len(client.requests[2].Tools) != 2 {
		t.Fatalf("tool sets: %+v", client.requests)
	}
	events, _ := store.Load(context.Background(), "s", 0)
	rejected := false
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.ErrorCode == "tool_activation_rejected" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("missing recoverable rejection")
	}
}
