package slackagent

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

func TestToolStepSetsDeterministicEnglishStatus(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Locale: "zh-CN"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.ToolStep([]model.ToolCall{{ID: "call", Name: "mcp_notion_fetch"}})

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.statuses; len(got) != 2 || got[0] != "Thinking" || got[1] != "Thinking" {
		t.Fatalf("statuses = %#v", got)
	}
	if got := messenger.loading; len(got) != 2 || len(got[1]) != 1 || got[1][0] != "Working" {
		t.Fatalf("loading messages = %#v", got)
	}
}

func TestToolStepDoesNotRepeatStatusForSameCall(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	call := model.ToolCall{ID: "call", Name: "artifact_read"}
	stream.ToolStep([]model.ToolCall{call})
	stream.ToolStep([]model.ToolCall{call})

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := len(messenger.statuses); got != 2 {
		t.Fatalf("status calls = %d, want 2", got)
	}
}
