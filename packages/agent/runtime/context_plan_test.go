package runtime

import (
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestForcedProjectionPreservesCurrentInputAndCompleteParallelBatch(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "Current constraints must remain explicit.")
	old := model.TextMessage(model.RoleAssistant, "Older completed investigation.")
	calls := model.Message{Role: model.RoleAssistant, Content: []model.Content{
		{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "a", Name: "read", Arguments: []byte(`{}`)}},
		{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "b", Name: "read", Arguments: []byte(`{}`)}},
	}}
	request := ProjectRequest{CompactHistory: true, Events: []transcript.Event{
		{Sequence: 1, TurnID: "turn", Type: transcript.UserInput, Message: &input},
		{Sequence: 2, TurnID: "turn", Type: transcript.AssistantMessage, Message: &old},
		{Sequence: 3, TurnID: "turn", Type: transcript.AssistantMessage, Message: &calls},
		toolResultEvent("turn", 4, "a", "evidence a"),
		toolResultEvent("turn", 5, "b", "evidence b"),
	}}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 12000, ReserveTokens: 1000}).Project(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Dropped) != 1 || projection.Dropped[0].Text() != old.Text() {
		t.Fatalf("summarized protected input or partial tool batch: %+v", projection.Dropped)
	}
	if len(projection.Messages) != 4 || projection.Messages[0].Text() != input.Text() || len(projection.Messages[1].ToolCalls()) != 2 {
		t.Fatalf("lost current input or batch: %+v", projection.Messages)
	}
	if projection.CoversThrough != 2 {
		t.Fatalf("coverage crosses the live batch: %d", projection.CoversThrough)
	}
}

func TestForcedProjectionDoesNotCreateCompactionFromOnlyCurrentInput(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "Do not compress an input that has never been sampled.")
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 12000}).Project(t.Context(), ProjectRequest{
		CompactHistory: true,
		Events:         []transcript.Event{{Sequence: 1, TurnID: "turn", Type: transcript.UserInput, Message: &input}},
	})
	if err != nil || len(projection.Dropped) != 0 || len(projection.Messages) != 1 || projection.Messages[0].Text() != input.Text() {
		t.Fatalf("unexpected projection: %+v error=%v", projection, err)
	}
}
