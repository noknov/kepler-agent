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

type recordingProgressModel struct{ request model.Request }

func (m *recordingProgressModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.request = request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"action":"Reading","target":"records"}`)}, nil
}

func TestProgressSummaryUsesBoundedToolFreeRequest(t *testing.T) {
	client := &recordingProgressModel{}
	summary, err := (&ProgressSummarizer{Client: client, Model: "mimo-v2.5"}).Summarize(context.Background(), "read records", []model.ToolCall{{Name: "artifact_read"}})
	if err != nil || summary != "Reading records" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if client.request.MaxOutputTokens != progressMaxOutputTokens || client.request.ReasoningEffort != "disabled" || len(client.request.Tools) != 0 {
		t.Fatalf("progress request = %+v", client.request)
	}
}

func TestToolStepReplacesInitialLoadingWithGeneratedStatus(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.progress = &ProgressSummarizer{Client: progressModel{}}
	stream.Start()
	stream.ToolStep([]model.ToolCall{{ID: "call", Name: "artifact_read"}})

	deadline := time.Now().Add(time.Second)
	for {
		messenger.mu.Lock()
		ready := len(messenger.statuses) == 2
		messenger.mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.statuses; len(got) != 2 || got[0] != initialThreadStatus || got[1] != initialThreadStatus {
		t.Fatalf("statuses = %#v", got)
	}
	if got := messenger.loading; len(got) != 2 || len(got[0]) != 0 || len(got[1]) != 1 || got[1][0] != "Reading records" {
		t.Fatalf("loading messages = %#v", got)
	}
}
