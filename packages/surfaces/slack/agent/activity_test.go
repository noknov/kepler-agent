package slackagent

import (
	"context"
	"testing"
	"time"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type assistantStatusMessenger struct {
	nativeStreamingMessenger
	activity    chan string
	unsupported bool
}

func (m *assistantStatusMessenger) SetAssistantStatus(_ context.Context, _, _, text string) error {
	if m.unsupported {
		return statusUnsupportedError{}
	}
	m.activity <- text
	return nil
}

type statusUnsupportedError struct{}

func (statusUnsupportedError) Error() string          { return "unsupported channel" }
func (statusUnsupportedError) SlackErrorCode() string { return "method_not_supported_for_channel_type" }

func TestCommentaryUsesTemporaryStatusAndClearsOnCompletion(t *testing.T) {
	m := &assistantStatusMessenger{activity: make(chan string, 10)}
	s := newSlackStream(context.Background(), m, slackconversation.Request{Channel: "C1", ThreadTS: "T1"})
	s.Start()
	<-m.activity
	s.UpdateActivity("Inspecting service. Authorization: Bearer xoxb-secret")
	select {
	case text := <-m.activity:
		if text != "Inspecting service. Authorization: Bearer [redacted]" {
			t.Fatalf("status=%q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("missing commentary status")
	}
	s.setSessionStatus(sessionActive)
	if text := <-m.activity; text != "" {
		t.Fatalf("terminal status=%q", text)
	}
	s.UpdateActivity("late commentary")
	s.sessionMu.Lock()
	s.sessionMu.Unlock()
	if s.answer.String() != "" || len(m.planPosts) != 0 || len(m.statuses) != 0 {
		t.Fatal("commentary leaked into answer, plan or modern status")
	}
}

func TestAssistantStatusUnsupportedChannelUsesSessionLifecycle(t *testing.T) {
	m := &assistantStatusMessenger{unsupported: true}
	s := newSlackStream(context.Background(), m, slackconversation.Request{Channel: "C1", ThreadTS: "T1"})
	s.Start()
	s.setSessionStatus(sessionActive)
	if len(m.statuses) != 2 || m.statuses[0] != sessionProcessing || m.statuses[1] != sessionActive {
		t.Fatalf("statuses=%v", m.statuses)
	}
}
