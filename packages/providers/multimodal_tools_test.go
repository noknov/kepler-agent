package providers

import (
	"github.com/noknov/kepler-agent/packages/agent/model"
	"testing"
)

func TestToolImagesSurviveCanonicalWireConversion(t *testing.T) {
	message := model.Message{Role: model.RoleTool, Content: []model.Content{{Type: model.ContentToolResult, ToolResult: &model.ToolResult{CallID: "c", Content: []model.Content{{Type: model.ContentText, Text: "screenshot"}, {Type: model.ContentImage, ImageURL: "data:image/png;base64,eA=="}}}}}}
	wire := toWireMessage(message)
	if len(wire.ContentParts) != 2 || wire.ContentParts[1].Type != "image_url" || wire.ContentParts[0].Text != "screenshot" {
		t.Fatalf("tool images lost: %+v", wire)
	}
}

func TestCommentaryAndNativeReasoningRemainDistinct(t *testing.T) {
	message := toolCallStepMessage(model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentText, Text: "inspect files"}, {Type: model.ContentReasoning, Text: "native reasoning"}, {Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "c", Name: "read"}}}})
	wire := toWireMessage(message)
	if message.Text() != "" || wire.Content != "inspect files" || wire.ReasoningContent != "native reasoning" {
		t.Fatalf("commentary mixed with reasoning: %+v", wire)
	}
}
