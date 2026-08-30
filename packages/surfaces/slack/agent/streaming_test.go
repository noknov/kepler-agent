package slackagent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/safety"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type nativeStreamingMessenger struct {
	mu       sync.Mutex
	posts    []string
	started  int
	appends  []string
	chunks   [][]map[string]any
	stopped  int
	updates  []string
	statuses []string
	startErr error
	start    []slackconversation.StreamStart
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
func (m *nativeStreamingMessenger) StartStream(_ context.Context, request slackconversation.StreamStart) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started++
	m.start = append(m.start, request)
	if m.startErr != nil {
		return "", m.startErr
	}
	return "1.0", nil
}

func TestPlanUpdatesUseSlackPlanDisplay(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1"})
	stream.Lifecycle(transcript.Event{Type: transcript.PlanUpdated, Plan: &tool.PlanUpdate{Explanation: "Investigating", Items: []tool.PlanItem{
		{ID: "inspect", Task: "Inspect logs", Status: "in_progress"},
		{ID: "verify", Task: "Verify the fix", Status: "pending"},
	}}})
	if messenger.started != 1 || messenger.start[0].TaskDisplayMode != "plan" {
		t.Fatalf("start = %#v", messenger.start)
	}
	if got := messenger.start[0].Chunks; len(got) != 3 || got[1]["status"] != "in_progress" || got[2]["status"] != "pending" {
		t.Fatalf("plan chunks = %#v", got)
	}
	stream.UpdatePlan(&tool.PlanUpdate{Items: []tool.PlanItem{{ID: "inspect", Task: "Inspect logs", Status: "completed"}}})
	if got := messenger.chunks; len(got) != 1 || len(got[0]) != 2 || got[0][1]["status"] != "complete" {
		t.Fatalf("appended plan chunks = %#v", got)
	}
}
func (m *nativeStreamingMessenger) AppendStream(_ context.Context, _, _ string, chunks []map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = append(m.chunks, chunks)
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
func (m *nativeStreamingMessenger) UpdateMarkdownMessage(_ context.Context, _, _ string, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, text)
	return nil
}
func (m *nativeStreamingMessenger) SetAgentSessionStatus(_ context.Context, _, _, _ string, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, status)
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

func TestStreamStartsWithFirstAssistantDelta(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1"})
	stream.Start()
	if messenger.started != 0 {
		t.Fatalf("started = %d, want no empty stream before assistant text", messenger.started)
	}
	stream.AppendDelta("hello")
	stream.flushStreamUpdate("hello", false)
	if messenger.started != 1 {
		t.Fatalf("started = %d, want stream start with assistant text", messenger.started)
	}
}

func TestStartStreamFailurePostsFinalAnswer(t *testing.T) {
	messenger := &nativeStreamingMessenger{startErr: context.Canceled}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", EventID: "Ev1"})
	stream.Start()
	if messenger.started != 0 {
		t.Fatalf("started = %d, want no start before assistant text", messenger.started)
	}
	stream.AppendDelta("hello")
	stream.flushStreamUpdate("hello", false)
	if messenger.started != 1 {
		t.Fatalf("started = %d, want one attempted stream start", messenger.started)
	}
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

func TestStreamDeliveryIsImmediate(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1", EventID: "Ev1"})
	stream.Start()
	stream.AppendDelta("hello")
	stream.flushStreamUpdate("hello", false)
	if messenger.started != 1 || len(messenger.appends) != 1 {
		t.Fatalf("started=%d appends=%#v, want immediate delivery", messenger.started, messenger.appends)
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

func TestNativeStreamSanitizesDeltasWithoutRewritingTheMessage(t *testing.T) {
	messenger := &nativeStreamingMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1"})
	stream.redactor = safety.NewStreamRedactor(safety.Redactor{})
	stream.Start()
	stream.AppendDelta("the xoxb-secret is hidden")
	if _, err := stream.Complete("the [redacted] is hidden"); err != nil {
		t.Fatal(err)
	}
	if len(messenger.appends) != 1 || messenger.appends[0] != "the [redacted] is hidden" {
		t.Fatalf("appends=%#v", messenger.appends)
	}
	if len(messenger.posts) != 0 {
		t.Fatalf("unexpected final rewrite: %#v", messenger.posts)
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

type failingAppendMessenger struct {
	nativeStreamingMessenger
	calls int
}

func (m *failingAppendMessenger) AppendStream(_ context.Context, _, _ string, chunks []map[string]any) error {
	m.calls++
	if m.calls == 2 {
		return fmt.Errorf("transient Slack append failure")
	}
	return m.nativeStreamingMessenger.AppendStream(context.Background(), "", "", chunks)
}

func TestNativeStreamFailureEditsExistingReply(t *testing.T) {
	messenger := &failingAppendMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C1", ThreadTS: "T1", UserID: "U1"})
	stream.Start()
	stream.AppendDelta("partial")
	stream.flushStreamUpdate("partial", false)
	stream.AppendDelta(" answer")
	stream.flushStreamUpdate("partial answer", false)
	ts, err := stream.Complete("final answer")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "1.0" || len(messenger.posts) != 0 || len(messenger.updates) != 1 || messenger.updates[0] != "final answer" {
		t.Fatalf("ts=%q posts=%#v updates=%#v", ts, messenger.posts, messenger.updates)
	}
}
