package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestBoundedProjectorDropsOldMessagesAndPreservesRecentTurn(t *testing.T) {
	events := []transcript.Event{
		{Sequence: 1, Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, strings.Repeat("old ", 100)))},
		{Sequence: 2, Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, strings.Repeat("answer ", 100)))},
		{Sequence: 3, Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "recent question"))},
		{Sequence: 4, Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, "recent answer"))},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 80, ReserveTokens: 10}).Project(context.Background(), events, model.TextMessage(model.RoleSystem, "system"))
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Dropped) == 0 || projection.CoversThrough == 0 {
		t.Fatalf("projection=%+v", projection)
	}
	if got := projection.Messages[len(projection.Messages)-1].Text(); got != "recent answer" {
		t.Fatalf("last message=%q", got)
	}
}

func TestProjectionUsesLatestCompactionCoverage(t *testing.T) {
	summary := model.TextMessage(model.RoleUser, "<untrusted_transcript_summary>\nsummary\n</untrusted_transcript_summary>")
	old := model.TextMessage(model.RoleUser, "old")
	newer := model.TextMessage(model.RoleUser, "new")
	events := []transcript.Event{
		{Sequence: 1, Type: transcript.UserInput, Message: &old},
		{Sequence: 2, Type: transcript.CompactionCreated, Message: &summary, Metadata: []byte(`{"covers_through":1}`)},
		{Sequence: 3, Type: transcript.UserInput, Message: &newer},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 1000}).Project(context.Background(), events, model.Message{})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Messages) != 3 || projection.Messages[0].Role != model.RoleSystem || projection.Messages[0].Text() != untrustedTranscriptSummaryBoundary.Text() || projection.Messages[1].Text() != summary.Text() || projection.Messages[2].Text() != "new" {
		t.Fatalf("messages=%+v", projection.Messages)
	}
}

func TestBoundedProjectorNeverSplitsToolCallAndResult(t *testing.T) {
	call := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call-1", Name: "read", Arguments: []byte(`{}`)}}}}
	result := toolResultEvent("turn-old", 3, "call-1", strings.Repeat("result ", 100))
	events := []transcript.Event{
		{Sequence: 1, TurnID: "turn-old", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, strings.Repeat("old ", 100)))},
		{Sequence: 2, TurnID: "turn-old", Type: transcript.AssistantMessage, Message: &call},
		result,
		{Sequence: 4, TurnID: "turn-new", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "new question"))},
		{Sequence: 5, TurnID: "turn-new", Type: transcript.AssistantMessage, Message: messagePtr(model.TextMessage(model.RoleAssistant, "new answer"))},
		{Sequence: 6, TurnID: "turn-last", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, "last question"))},
	}
	projection, err := NewBoundedProjector(ContextConfig{MaxTokens: 80}).Project(context.Background(), events, model.Message{})
	if err != nil {
		t.Fatal(err)
	}
	var sawCall, sawResult bool
	for _, message := range projection.Messages {
		sawCall = sawCall || len(message.ToolCalls()) > 0
		for _, content := range message.Content {
			sawResult = sawResult || content.ToolResult != nil
		}
	}
	if sawCall != sawResult {
		t.Fatalf("tool call/result pair was split: messages=%+v", projection.Messages)
	}
}

func TestEstimateTokensCountsCJKAndInlineImages(t *testing.T) {
	text := EstimateTokens([]model.Message{model.TextMessage(model.RoleUser, strings.Repeat("中", 100))})
	image := EstimateTokens([]model.Message{{Role: model.RoleUser, Content: []model.Content{{Type: model.ContentImage, ImageURL: "data:image/png;base64," + strings.Repeat("A", 4000)}}}})
	if text < 100 || image < 2000 {
		t.Fatalf("unexpected estimates: text=%d image=%d", text, image)
	}
}

func toolResultEvent(turnID string, sequence uint64, callID, text string) transcript.Event {
	call := tool.Call{ID: callID, Name: "read"}
	result := tool.Result{Content: []model.Content{{Type: model.ContentText, Text: text}}}
	return transcript.Event{Sequence: sequence, TurnID: turnID, Type: transcript.ToolCallCompleted, ToolCall: &call, ToolResult: &result}
}

func messagePtr(message model.Message) *model.Message { return &message }
