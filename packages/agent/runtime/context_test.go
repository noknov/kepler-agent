package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
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
	summary := model.TextMessage(model.RoleSystem, "summary")
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
	if len(projection.Messages) != 2 || projection.Messages[0].Text() != "summary" || projection.Messages[1].Text() != "new" {
		t.Fatalf("messages=%+v", projection.Messages)
	}
}

func messagePtr(message model.Message) *model.Message { return &message }
