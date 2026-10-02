package slackagent

import (
	"context"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"strings"
	"testing"
	"time"
)

type blockedStreamMessenger struct {
	nativeStreamingMessenger
	entered chan struct{}
	release chan struct{}
}

func (m *blockedStreamMessenger) StartStream(ctx context.Context, req slackconversation.StreamStart) (string, error) {
	close(m.entered)
	select {
	case <-m.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return m.nativeStreamingMessenger.StartStream(ctx, req)
}

func TestSlowSlackDeliveryDoesNotBlockDeltasAndFinalFlushDrains(t *testing.T) {
	messenger := &blockedStreamMessenger{entered: make(chan struct{}), release: make(chan struct{})}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T"})
	released := false
	defer func() {
		if !released {
			close(messenger.release)
		}
	}()
	returned := make(chan struct{})
	go func() { stream.AppendDelta("hello"); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("model callback blocked on Slack")
	}
	select {
	case <-messenger.entered:
	case <-time.After(time.Second):
		t.Fatal("first delta was not scheduled")
	}
	for range 100 {
		stream.AppendDelta(".")
	}
	final := "hello" + strings.Repeat(".", 100)
	done := make(chan error, 1)
	go func() { _, err := stream.Complete(final); done <- err }()
	close(messenger.release)
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("final delivery did not drain")
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	text := ""
	for _, start := range messenger.start {
		for _, chunk := range start.Chunks {
			text += chunk["text"].(string)
		}
	}
	for _, delta := range messenger.appends {
		text += delta
	}
	if text != final || messenger.started != 1 || messenger.stopped != 1 {
		t.Fatalf("delivery: text=%q starts=%d stops=%d", text, messenger.started, messenger.stopped)
	}
	if len(messenger.appends) > 1 {
		t.Fatalf("100 deltas were not coalesced: %d appends", len(messenger.appends))
	}
}
