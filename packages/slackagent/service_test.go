package slackagent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/hosted"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	v2runtime "github.com/noknov/slack-copilot-agent/packages/agentv2/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slackconversation"
)

type replyModel struct{}

func (replyModel) Generate(_ context.Context, _ model.Request, sink model.EventSink) (model.Response, error) {
	_ = sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "hello"})
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "hello"), FinishReason: model.FinishStop}, nil
}

type fakeMessenger struct {
	mu       sync.Mutex
	chunks   []map[string]any
	stopped  bool
	posts    []string
	statuses []string
	loading  [][]string
}

type memoryQueue struct {
	mu    sync.Mutex
	items map[string][]slackconversation.Request
}

func (q *memoryQueue) Enqueue(_ context.Context, session string, request slackconversation.Request) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.items == nil {
		q.items = make(map[string][]slackconversation.Request)
	}
	q.items[session] = append(q.items[session], request)
	return nil
}
func (q *memoryQueue) Drain(_ context.Context, session string) ([]slackconversation.Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := append([]slackconversation.Request(nil), q.items[session]...)
	delete(q.items, session)
	return out, nil
}

func (m *fakeMessenger) PostMessage(_ context.Context, _, _, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts = append(m.posts, text)
	return "post", nil
}
func (m *fakeMessenger) StartStream(context.Context, string, string, string) (string, error) {
	return "stream", nil
}
func (m *fakeMessenger) AppendStream(_ context.Context, _ string, _ string, chunks []map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = append(m.chunks, chunks...)
	return nil
}
func (m *fakeMessenger) StopStream(context.Context, string, string) error {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	return nil
}
func (m *fakeMessenger) DeleteMessage(context.Context, string, string) error { return nil }
func (m *fakeMessenger) ThreadContext(context.Context, string, string, int) string {
	return "prior Slack context"
}
func (m *fakeMessenger) SetThreadStatus(_ context.Context, _, _, status string, loading []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, status)
	m.loading = append(m.loading, append([]string(nil), loading...))
	return nil
}

func TestServiceRunsHostedHarnessAndStreamsAnswer(t *testing.T) {
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	runner, err := v2runtime.New(v2runtime.Config{Model: "test"}, v2runtime.Dependencies{Model: replyModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
	if err != nil {
		t.Fatal(err)
	}
	service.Agent.Runtime = runner
	if !service.HandleMention(context.Background(), slackconversation.Request{EventID: "event-1", UserID: "U1", Channel: "C1", ThreadTS: "T1", Text: "question"}) {
		t.Fatal("request was not handled")
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if !messenger.stopped {
		t.Fatal("stream was not finalized")
	}
	found := false
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "task_update" {
			t.Fatalf("native Slack path rendered a task card: %#v", messenger.chunks)
		}
		if chunk["type"] == "markdown_text" && chunk["text"] == "hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stream chunks = %#v", messenger.chunks)
	}
	if len(messenger.statuses) < 2 || messenger.statuses[0] != "is thinking" || messenger.statuses[len(messenger.statuses)-1] != "" {
		t.Fatalf("thread statuses = %#v", messenger.statuses)
	}
}

func TestLifecycleStatusUsesCanonicalEventsAndCatalogFallback(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "diagnose"})
	stream.Start()
	if len(messenger.statuses) != 0 {
		t.Fatalf("Start emitted status without a runtime event: %#v", messenger.statuses)
	}
	stream.Lifecycle(transcript.Event{Type: transcript.TurnStarted})
	stream.Lifecycle(transcript.Event{Type: transcript.ToolCallStarted, ToolCall: &tool.Call{Name: "repo-search"}})
	stream.Lifecycle(transcript.Event{Type: transcript.TurnCompleted})
	if got := messenger.statuses; len(got) != 3 || got[0] != "is thinking" || got[1] != "is working" || got[2] != "" {
		t.Fatalf("statuses=%#v", got)
	}
	if got := messenger.loading[1]; len(got) != 1 || got[0] != "Using repo search..." {
		t.Fatalf("tool loading=%#v", got)
	}
}

func TestLifecycleStatusShowsOnlyRetryableModelFailures(t *testing.T) {
	retryable := json.RawMessage(`{"retryable":true}`)
	if status, _, ok := lifecycleStatus(transcript.Event{Type: transcript.ModelFailed, Metadata: retryable}, false); !ok || status != "is retrying" {
		t.Fatalf("retryable failure status=%q ok=%v", status, ok)
	}
	if _, _, ok := lifecycleStatus(transcript.Event{Type: transcript.ModelFailed, Metadata: json.RawMessage(`{"retryable":false}`)}, false); ok {
		t.Fatal("non-retryable model failure emitted an intermediate status")
	}
}

func TestRenderAnswerUsesStructuredCitations(t *testing.T) {
	message := model.Message{Role: model.RoleAssistant, Content: []model.Content{{
		Type: model.ContentText,
		Text: "Supported claim.",
		Citations: []model.Citation{
			{URL: "https://example.test/source", Title: "Primary source"},
			{URL: "https://example.test/source", Title: "duplicate"},
		},
	}}}
	want := "Supported claim.\n\nSources: [Primary source](https://example.test/source)"
	if got := renderAnswer(message); got != want {
		t.Fatalf("renderAnswer() = %q, want %q", got, want)
	}
}

func TestQueuedInputSurvivesServiceReplacement(t *testing.T) {
	queue := &memoryQueue{}
	first := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	first.Queue = queue
	active := &activeRun{}
	request := slackconversation.Request{EventID: "event-queued", UserID: "U1", Text: "follow up"}
	first.enqueue("session", active, request)

	second := New(hosted.Agent{}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	second.Queue = queue
	queued := second.drainQueue("session", nil)
	if len(queued) != 1 || queued[0].EventID != request.EventID {
		t.Fatalf("queued=%+v", queued)
	}
}
