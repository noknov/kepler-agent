package conversationv2

import (
	"context"
	"sync"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/hosted"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	v2runtime "github.com/noknov/slack-copilot-agent/packages/agentv2/runtime"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	"github.com/noknov/slack-copilot-agent/packages/safety"
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
func (m *fakeMessenger) SetThreadStatus(_ context.Context, _, _, status string, _ []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, status)
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
	if !service.HandleMention(context.Background(), conversation.Request{EventID: "event-1", UserID: "U1", Channel: "C1", ThreadTS: "T1", Text: "question"}) {
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
