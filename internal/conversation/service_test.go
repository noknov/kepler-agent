package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
)

func TestHandleReplyIgnoresThreadWithoutPendingQuestion(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewRecorder()
	svc := NewService(
		store,
		&fakeMessenger{},
		agent.Runner{LLM: &replyLLM{}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		metrics,
	)

	handled := svc.HandleReply(ctx, Request{
		EventID:  "E1",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "the answer",
	})

	if handled {
		t.Fatal("HandleReply() handled a thread without pending input")
	}
	if got := metrics.Snapshot().Requests; got != 0 {
		t.Fatalf("Requests = %d, want 0", got)
	}
}

func TestHandleReplyConsumesPendingQuestion(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("C1", "100.000")
	if err := store.Save(ctx, session.Session{
		ID:               id,
		Channel:          "C1",
		ThreadTS:         "100.000",
		UserID:           "U1",
		PendingUserInput: true,
		PendingUserID:    "U1",
		PendingQuestion:  "which environment?",
	}); err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewRecorder()
	svc := NewService(
		store,
		&fakeMessenger{},
		agent.Runner{LLM: &replyLLM{}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		metrics,
	)

	handled := svc.HandleReply(ctx, Request{
		EventID:  "E2",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "production",
	})

	if !handled {
		t.Fatal("HandleReply() did not consume pending input")
	}
	sess, ok, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("session not saved")
	}
	if sess.PendingUserInput || sess.PendingUserID != "" || sess.PendingQuestion != "" {
		t.Fatalf("pending state was not cleared: %#v", sess)
	}
	if len(sess.Turns) == 0 || sess.Turns[0].Content != "production" {
		t.Fatalf("user reply was not persisted: %#v", sess.Turns)
	}
	if got := metrics.Snapshot().Requests; got != 1 {
		t.Fatalf("Requests = %d, want 1", got)
	}
}

type replyLLM struct{}

func (replyLLM) Chat(_ llm.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: "ack"},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}, nil
}

type fakeMessenger struct {
	posts []string
}

func (m *fakeMessenger) PostMessage(_ context.Context, _, _, text string) (string, error) {
	m.posts = append(m.posts, text)
	return "200.000", nil
}

func (m *fakeMessenger) StartStream(context.Context, string, string, string) (string, error) {
	return "", errors.New("stream unavailable")
}

func (m *fakeMessenger) AppendStream(context.Context, string, string, []map[string]any) error {
	return nil
}

func (m *fakeMessenger) StopStream(context.Context, string, string) error {
	return nil
}

func (m *fakeMessenger) DeleteMessage(context.Context, string, string) error {
	return nil
}

func (m *fakeMessenger) ThreadContext(context.Context, string, string, int) string {
	return ""
}
