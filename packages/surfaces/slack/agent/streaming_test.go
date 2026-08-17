package slackagent

import (
	"context"
	"sync"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

type streamingMessenger struct {
	mu      sync.Mutex
	posts   []string
	updates []string
}

func (m *streamingMessenger) PostMessage(context.Context, string, string, string) (string, error) {
	return "1.0", nil
}
func (m *streamingMessenger) PostMarkdownMessage(context.Context, string, string, string) (string, error) {
	return "1.0", nil
}
func (m *streamingMessenger) PostMarkdownMessageWithID(_ context.Context, _, _, text, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, text)
	return "1.0", nil
}
func (m *streamingMessenger) UpdateMarkdownMessage(_ context.Context, _, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, text)
	return nil
}

func TestSlackStreamUpdatesIncrementally(t *testing.T) {
	messenger := &streamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", EventID: "Ev1"})
	stream.AppendDelta("hel")
	stream.flushStreamUpdate("hel")
	stream.AppendDelta("lo")
	stream.flushStreamUpdate("hello")
	if len(messenger.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(messenger.posts))
	}
	if len(messenger.updates) == 0 {
		t.Fatal("expected at least one update")
	}
	ts, err := stream.Complete("hello final")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "1.0" {
		t.Fatalf("ts = %q", ts)
	}
}
