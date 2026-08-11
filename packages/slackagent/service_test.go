package slackagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/hosted"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	agentruntime "github.com/noknov/slack-copilot-agent/packages/agent/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/slackconversation"
)

type replyModel struct{ request *model.Request }

func (m *replyModel) Generate(_ context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	m.request = &request
	_ = sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "hello"})
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "hello"), FinishReason: model.FinishStop}, nil
}

type fakeMessenger struct {
	mu        sync.Mutex
	chunks    []map[string]any
	stopped   bool
	posts     []string
	statuses  []string
	loading   [][]string
	appendErr error
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
	if m.appendErr != nil {
		return m.appendErr
	}
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
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore(), Events: service.EventSink()})
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
	if client.request == nil || len(client.request.Messages) == 0 || !strings.Contains(client.request.Messages[0].Text(), "transient Slack status") {
		t.Fatalf("hosted progress instruction missing from model request: %+v", client.request)
	}
	if len(messenger.statuses) < 2 || messenger.statuses[0] != "is thinking" || messenger.statuses[len(messenger.statuses)-1] != "" {
		t.Fatalf("thread statuses = %#v", messenger.statuses)
	}
}

func TestHandleReplyOnlyConsumesPendingInputFromItsOwner(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	client := &replyModel{}
	runner, _ := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store, Events: service.EventSink()})
	service.Agent.Runtime = runner
	sessionID := session.ID("C", "T")
	metadata := json.RawMessage(`{"user_id":"U1"}`)
	_, _ = store.Append(context.Background(), transcript.Event{ID: "start", SessionID: sessionID, TurnID: "first", Type: transcript.TurnStarted, Metadata: metadata})
	_, _ = store.Append(context.Background(), transcript.Event{ID: "done", SessionID: sessionID, TurnID: "first", Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingInput)})
	if service.HandleReply(context.Background(), slackconversation.Request{EventID: "wrong", UserID: "U2", Channel: "C", ThreadTS: "T", Text: "production"}) {
		t.Fatal("reply from a different user was consumed")
	}
	if !service.HandleReply(context.Background(), slackconversation.Request{EventID: "right", UserID: "U1", Channel: "C", ThreadTS: "T", Text: "production"}) {
		t.Fatal("pending reply from the owner was ignored")
	}
}

func TestLifecycleStatusUsesCanonicalEvents(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "diagnose"})
	stream.Start()
	if len(messenger.statuses) != 0 {
		t.Fatalf("Start emitted status without a runtime event: %#v", messenger.statuses)
	}
	stream.Lifecycle(transcript.Event{Type: transcript.TurnStarted})
	stream.Lifecycle(transcript.Event{Type: transcript.ToolCallStarted, ToolCall: &tool.Call{Name: "repo-search"}})
	stream.Lifecycle(transcript.Event{Type: transcript.TurnCompleted})
	if got := messenger.statuses; len(got) != 2 || got[0] != "is thinking" || got[1] != "" {
		t.Fatalf("statuses=%#v", got)
	}
	if got := messenger.loading[0]; len(got) != 1 || got[0] != "Thinking..." {
		t.Fatalf("thinking loading=%#v", got)
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

func TestToolStepUsesPrimaryModelSummaryAndKeepsItDuringToolLifecycle(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "查一下支付服务"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.Write("正在查询支付服务的部署记录")
	stream.CommitStep(model.Message{Role: model.RoleAssistant, Content: []model.Content{
		{Type: model.ContentText, Text: "正在查询支付服务的部署记录"},
		{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "notion-search"}},
	}})
	stream.Lifecycle(transcript.Event{Type: transcript.ToolCallStarted, ToolCall: &tool.Call{Name: "notion-search"}})
	if got := messenger.loading; len(got) != 2 || len(got[1]) != 1 || got[1][0] != "正在查询支付服务的部署记录" {
		t.Fatalf("loading messages=%#v", got)
	}
	if len(messenger.chunks) != 0 {
		t.Fatalf("step summary leaked into answer stream: %#v", messenger.chunks)
	}
}

func TestToolLifecycleWithoutPrimarySummaryKeepsThinkingStatus(t *testing.T) {
	if _, _, ok := lifecycleStatus(transcript.Event{Type: transcript.ToolCallStarted, ToolCall: &tool.Call{Name: "private_internal_tool"}}, true); ok {
		t.Fatal("tool lifecycle replaced the thinking fallback")
	}
}

func TestUnsupportedImagesAreRemovedWithoutMutatingInput(t *testing.T) {
	input := model.Message{Role: model.RoleUser, Content: []model.Content{
		{Type: model.ContentText, Text: "看看"},
		{Type: model.ContentImage, ImageURL: "https://example.test/image.png"},
	}}
	got := withoutUnsupportedImages(input, true)
	if len(input.Content) != 2 || len(got.Content) != 2 || got.Content[0].Text != "看看" || !strings.Contains(got.Content[1].Text, "1 个图片附件") {
		t.Fatalf("input=%+v got=%+v", input.Content, got.Content)
	}
	for _, block := range got.Content {
		if block.Type == model.ContentImage {
			t.Fatalf("image was retained: %+v", got.Content)
		}
	}
}

func TestCompletePostsFallbackWhenStreamDeliveryFails(t *testing.T) {
	messenger := &fakeMessenger{appendErr: errors.New("stream unavailable")}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", UserID: "U"})
	stream.Write("answer")
	stream.CommitStep(model.TextMessage(model.RoleAssistant, "answer"))
	stream.Complete("answer")
	if len(messenger.posts) != 1 || messenger.posts[0] != "answer" || !messenger.stopped {
		t.Fatalf("posts=%#v stopped=%v", messenger.posts, messenger.stopped)
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
