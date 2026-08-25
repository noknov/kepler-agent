package slackagent

import (
	"context"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

type progressModel struct{}

func (progressModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"action":"Reading","target":"records"}`)}, nil
}

func TestToolStepAddsGeneratedStatusWithoutInitialStatus(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.progress = &ProgressSummarizer{Client: progressModel{}}
	stream.Start()
	stream.ToolStep([]model.ToolCall{{ID: "call", Name: "artifact_read"}})

	deadline := time.Now().Add(time.Second)
	for {
		messenger.mu.Lock()
		ready := len(messenger.statuses) == 1
		messenger.mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.statuses; len(got) != 1 || got[0] != "is working" {
		t.Fatalf("statuses = %#v", got)
	}
	if got := messenger.loading; len(got) != 1 || len(got[0]) != 1 || got[0][0] != "Reading records" {
		t.Fatalf("loading messages = %#v", got)
	}
}
