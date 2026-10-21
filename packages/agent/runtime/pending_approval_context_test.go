package runtime

import (
	"context"
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"testing"
)

func TestPendingApprovalProjectsOneNonExecutionResult(t *testing.T) {
	call := tool.Call{ID: "pending", Name: "write", Arguments: json.RawMessage(`{}`)}
	assistant := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}}}}
	user := model.TextMessage(model.RoleUser, "continue")
	events := []transcript.Event{
		{Sequence: 1, TurnID: "old", Type: transcript.AssistantMessage, Message: &assistant},
		{Sequence: 2, TurnID: "old", Type: transcript.ApprovalRequested, ToolCall: &call},
		{Sequence: 3, TurnID: "new", Type: transcript.UserInput, Message: &user},
	}
	for _, resolved := range []bool{false, true} {
		current := append([]transcript.Event(nil), events...)
		if resolved {
			result := tool.TextResult("declined")
			result.IsError = true
			current = append(current, transcript.Event{Sequence: 4, TurnID: "old", Type: transcript.ToolCallFailed, ToolCall: &call, ToolResult: &result})
		}
		projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 4000}).Project(context.Background(), ProjectRequest{Events: current})
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, message := range projection.Messages {
			for _, block := range message.Content {
				if block.ToolResult != nil && block.ToolResult.CallID == call.ID {
					count++
					if !block.ToolResult.IsError {
						t.Fatal("approval was represented as a successful execution")
					}
					if resolved && block.ToolResult.Content[0].Text != "declined" {
						t.Fatal("pending placeholder hid the recorded resolution")
					}
				}
			}
		}
		if count != 1 {
			t.Fatalf("resolved=%v results=%d", resolved, count)
		}
	}
}
