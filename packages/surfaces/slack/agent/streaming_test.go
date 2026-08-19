package slackagent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

type nativeStreamingMessenger struct {
	mu       sync.Mutex
	posts    []string
	started  int
	appends  []string
	stopped  int
	startErr error
}

func (m *nativeStreamingMessenger) PostMessage(context.Context, string, string, string) (string, error) {
	return "1.0", nil
}
func (m *nativeStreamingMessenger) PostMarkdownMessage(_ context.Context, _, _, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, text)
	return "1.0", nil
}
func (m *nativeStreamingMessenger) PostMarkdownMessageWithID(_ context.Context, _, _, text, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, text)
	return "1.0", nil
}
func (m *nativeStreamingMessenger) StartStream(context.Context, string, string, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started++
	if m.startErr != nil {
		return "", m.startErr
	}
	return "1.0", nil
}
func (m *nativeStreamingMessenger) AppendStream(_ context.Context, _, _ string, chunks []map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, chunk := range chunks {
		if text, _ := chunk["text"].(string); text != "" {
			m.appends = append(m.appends, text)
		}
	}
	return nil
}
func (m *nativeStreamingMessenger) StopStream(context.Context, string, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped++
	return nil
}

func TestNativeSlackStreamAppendsIncrementally(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1", EventID: "Ev1"})
	stream.Start()
	stream.AppendDelta("hel")
	stream.flushStreamUpdate("hel", false)
	stream.AppendDelta("lo")
	stream.flushStreamUpdate("hello", false)
	if messenger.started != 1 {
		t.Fatalf("started = %d, want 1", messenger.started)
	}
	if len(messenger.appends) != 2 || messenger.appends[0] != "hel" || messenger.appends[1] != "lo" {
		t.Fatalf("appends = %#v", messenger.appends)
	}
	ts, err := stream.Complete("hello")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "1.0" || messenger.stopped != 1 {
		t.Fatalf("ts=%q stopped=%d", ts, messenger.stopped)
	}
}

func TestStreamStartsAtTurnStart(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1"})
	stream.Start()
	if messenger.started != 1 {
		t.Fatalf("started = %d, want 1 at turn start", messenger.started)
	}
}

func TestStartStreamFailurePostsFinalAnswer(t *testing.T) {
	messenger := &nativeStreamingMessenger{startErr: context.Canceled}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", EventID: "Ev1"})
	stream.Start()
	if messenger.started != 1 {
		t.Fatalf("started = %d, want 1", messenger.started)
	}
	stream.AppendDelta("hello")
	stream.flushStreamUpdate("hello", false)
	if len(messenger.appends) != 0 {
		t.Fatalf("appends = %#v, want none", messenger.appends)
	}
	if _, err := stream.Complete("hello"); err != nil {
		t.Fatal(err)
	}
	if len(messenger.posts) != 1 || messenger.posts[0] != "hello" {
		t.Fatalf("posts = %#v", messenger.posts)
	}
}

func TestNativeCompleteAppendsSourcesSuffix(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1"})
	stream.Start()
	stream.AppendDelta("answer")
	stream.flushStreamUpdate("answer", false)
	final := "answer\n\nSources: [doc](https://example.test)"
	if _, err := stream.Complete(final); err != nil {
		t.Fatal(err)
	}
	if got := messenger.appends[len(messenger.appends)-1]; got != "\n\nSources: [doc](https://example.test)" {
		t.Fatalf("final append = %q", got)
	}
	if messenger.stopped != 1 {
		t.Fatalf("stopped = %d, want 1", messenger.stopped)
	}
}

func TestStreamDeliveryDefersWhileProgressRuns(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1", EventID: "Ev1"})
	stream.Start()
	stream.progressRunning = true
	stream.AppendDelta("hello")
	stream.flushStreamUpdate("hello", false)
	if messenger.started != 1 || len(messenger.appends) != 0 {
		t.Fatalf("started=%d appends=%#v, want stream open but answer deferred", messenger.started, messenger.appends)
	}
	stream.progressRunning = false
	stream.flushDeferredStream(false)
	if len(messenger.appends) != 1 {
		t.Fatalf("appends=%#v, want delivery after progress finished", messenger.appends)
	}
}

func TestStreamSuffix(t *testing.T) {
	if got := streamSuffix("hello", "hello world"); got != " world" {
		t.Fatalf("suffix = %q", got)
	}
	if got := streamSuffix("", "hello"); got != "hello" {
		t.Fatalf("suffix = %q", got)
	}
}

type restartStreamingMessenger struct {
	nativeStreamingMessenger
	appendCalls int
}

func (m *restartStreamingMessenger) AppendStream(_ context.Context, _, _ string, chunks []map[string]any) error {
	m.appendCalls++
	if m.appendCalls == 1 {
		return fmt.Errorf("slack chat.appendStream failed: not_in_streaming_state")
	}
	return m.nativeStreamingMessenger.AppendStream(context.Background(), "", "", chunks)
}

func TestNativeStreamRestartsOnNotInStreamingState(t *testing.T) {
	messenger := &restartStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1"})
	stream.Start()
	stream.AppendDelta("hello")
	stream.flushStreamUpdate("hello", false)
	if messenger.started != 2 {
		t.Fatalf("started = %d, want restart after not_in_streaming_state", messenger.started)
	}
	if len(messenger.appends) != 1 || messenger.appends[0] != "hello" {
		t.Fatalf("appends = %#v", messenger.appends)
	}
}
