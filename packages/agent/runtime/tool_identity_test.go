package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestRunTurnRejectsAmbiguousToolIdentityBeforeAnyWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		ids  []string
	}{
		{"empty", []string{"valid", ""}},
		{"duplicate", []string{"same", "same"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var writes int
			catalog, _ := tool.NewCatalog(countingWriteTool{calls: &writes})
			message := model.Message{Role: model.RoleAssistant}
			for _, id := range test.ids {
				call := model.ToolCall{ID: id, Name: "write", Arguments: json.RawMessage(`{}`)}
				message.Content = append(message.Content, model.Content{Type: model.ContentToolCall, ToolCall: &call})
			}
			client := &scriptedModel{responses: []model.Response{{Message: message}, {Message: model.TextMessage(model.RoleAssistant, "done")}}}
			store := transcript.NewMemoryStore()
			runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Policy: tool.AllowAllPolicy{}, Transcript: store})
			_, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "write")})
			if model.ErrorKindOf(err) != model.ErrorProtocol || writes != 0 {
				t.Errorf("err=%v writes=%d; want protocol failure before writes", err, writes)
			}
			events, _ := store.Load(context.Background(), "s", 0)
			for _, event := range events {
				if event.Type == transcript.AssistantMessage && event.Message != nil && len(event.Message.ToolCalls()) > 0 {
					t.Error("persisted ambiguous executable message")
				}
			}
		})
	}
}

func TestRunTurnBindsReplayedToolResultToCall(t *testing.T) {
	for _, test := range []struct {
		name, toolName, args string
		wantError            bool
	}{
		{"changed arguments", "write", `{"target":"other"}`, true},
		{"changed tool", "echo", `{"target":"original","n":9007199254740993}`, true},
		{"changed exact integer", "write", `{"target":"original","n":9007199254740992}`, true},
		{"JSONB reordered equivalent", "write", `{ "n":9007199254740993.0, "target":"original" }`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := transcript.NewMemoryStore()
			call := tool.Call{ID: "one", Name: "write", Arguments: json.RawMessage(`{"target":"original","n":9007199254740993}`), Scope: tool.Scope{SessionID: "s", TurnID: "t"}}
			result := tool.TextResult("original result")
			result.Usage = &model.Usage{InputTokens: 17, Reported: true}
			result.Plan = &tool.PlanUpdate{Explanation: "already applied", Items: []tool.PlanItem{{Task: "original", Status: "completed"}}}
			for _, event := range []transcript.Event{{SessionID: "s", Type: transcript.SessionStarted}, {SessionID: "s", TurnID: "t", Type: transcript.TurnStarted}, {SessionID: "s", TurnID: "t", Type: transcript.ToolCallCompleted, ToolCall: &call, ToolResult: &result}} {
				if _, err := store.Append(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			var writes int
			catalog, _ := tool.NewCatalog(countingWriteTool{calls: &writes}, echoTool{})
			modelCall := model.ToolCall{ID: "one", Name: test.toolName, Arguments: json.RawMessage(test.args)}
			newCall := model.ToolCall{ID: "new-write", Name: "write", Arguments: json.RawMessage(`{}`)}
			content := []model.Content{{Type: model.ContentToolCall, ToolCall: &modelCall}}
			if test.wantError {
				content = append([]model.Content{{Type: model.ContentToolCall, ToolCall: &newCall}}, content...)
			}
			client := &scriptedModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content}}, {Message: model.TextMessage(model.RoleAssistant, "done")}}}
			runner, _ := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Policy: tool.AllowAllPolicy{}, Transcript: store})
			turn, err := runner.RunTurn(ctx, TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "write")})
			if test.wantError && model.ErrorKindOf(err) != model.ErrorProtocol {
				t.Errorf("accepted different call under existing ID: %v", err)
			}
			if !test.wantError && err != nil {
				t.Errorf("rejected equivalent durable call: %v", err)
			}
			if writes != 0 {
				t.Errorf("replayed completed write %d times", writes)
			}
			if turn.Usage.InputTokens != 17 {
				t.Errorf("recharged durable tool usage: %+v", turn.Usage)
			}
			events, _ := store.Load(ctx, "s", 0)
			for _, event := range events {
				if event.Type == transcript.PlanUpdated {
					t.Error("reapplied plan transition from durable result")
				}
			}

		})
	}
}
