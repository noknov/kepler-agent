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
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "Reading records")}, nil
}

type recordingProgressModel struct{ request model.Request }

func (m *recordingProgressModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.request = request
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "Reading records")}, nil
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

func TestDecodeProgressAcceptsSingleLineLabel(t *testing.T) {
	if got := decodeProgress(" Reading records "); got != "Reading records" {
		t.Fatalf("decodeProgress() = %q", got)
	}
	if got := decodeProgress("Reading\nrecords"); got != "" {
		t.Fatalf("decodeProgress() accepted multiline label: %q", got)
	}
}

func TestProgressErrorOutcomeDoesNotExposeProviderDetails(t *testing.T) {
	err := &model.Error{Kind: model.ErrorRateLimited, Message: "provider response body must stay private", StatusCode: 429}
	if got := progressErrorOutcome(err); got != "rate_limited" {
		t.Fatalf("progressErrorOutcome() = %q", got)
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

func TestStatusRefreshKeepsCurrentLoadingVisible(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.Start()
	stream.setProgressStatus(stream.statusEpoch, "Reading records")
	stream.refreshStatus()
	stream.stopStatusRefresh()

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.statuses; len(got) != 3 || got[2] != initialThreadStatus {
		t.Fatalf("statuses = %#v", got)
	}
	if got := messenger.loading; len(got) != 3 || len(got[2]) != 1 || got[2][0] != "Reading records" {
		t.Fatalf("loading messages = %#v", got)
	}
}

func TestClearStatusStopsRefresh(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	stream.Start()
	stream.clearStatus()
	stream.mu.Lock()
	timer := stream.statusTimer
	stream.mu.Unlock()
	if timer != nil {
		t.Fatal("status refresh timer remains armed after clear")
	}
}
