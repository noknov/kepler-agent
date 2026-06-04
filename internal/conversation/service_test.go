package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/slack"
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
	if sess.PendingUserInput || sess.PendingUserID != "" {
		t.Fatalf("pending state was not cleared: %#v", sess)
	}
	if len(sess.Turns) == 0 || sess.Turns[0].Content != "production" {
		t.Fatalf("user reply was not persisted: %#v", sess.Turns)
	}
	if got := metrics.Snapshot().Requests; got != 1 {
		t.Fatalf("Requests = %d, want 1", got)
	}
}

func TestUserFacingErrorForMaxToolSteps(t *testing.T) {
	got := userFacingError("err-test123")
	if strings.Contains(got, "agent exceeded max tool steps") {
		t.Fatalf("userFacingError() leaked internal error: %q", got)
	}
	if strings.Contains(got, "连续使用工具超过上限") {
		t.Fatalf("userFacingError() leaked implementation detail: %q", got)
	}
	if strings.Contains(got, "找我") || strings.Contains(strings.ToLower(got), "contact me") {
		t.Fatalf("userFacingError() should not ask users to contact a person: %q", got)
	}
	if !strings.Contains(got, "Something went wrong") || !strings.Contains(got, "Please try again later") || !strings.Contains(got, "Error ID: err-test123") {
		t.Fatalf("userFacingError() = %q, want generic error with id", got)
	}
}

func TestNewErrorID(t *testing.T) {
	got := newErrorID()
	if !strings.HasPrefix(got, "err-") || len(got) < 8 {
		t.Fatalf("newErrorID() = %q, want err-*", got)
	}
}

func TestStreamModeAnswerInStream(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: &replyLLM{content: "Author: @U085SRJFCLX"}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)
	svc.Format = slack.MarkdownToMrkdwn

	svc.HandleMention(ctx, Request{
		EventID:  "E3",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "who is the author?",
	})

	// Final answer should be in the stream, not a separate PostMessage
	for _, p := range messenger.posts {
		if p == "Author: <@U085SRJFCLX>" {
			t.Fatal("stream mode should NOT post final answer as separate message")
		}
	}
	foundText := false
	foundComplete := false
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "markdown_text" && chunk["text"] == "Author: <@U085SRJFCLX>" {
			foundText = true
		}
		if chunk["type"] == "task_update" && chunk["status"] == "complete" && chunk["title"] == "Done" {
			foundComplete = true
		}
	}
	if !foundText {
		t.Fatalf("stream should contain final answer as markdown_text: %#v", messenger.chunks)
	}
	if !foundComplete {
		t.Fatalf("stream should mark task complete: %#v", messenger.chunks)
	}
}

type streamLLM struct {
	content string
}

func (l *streamLLM) Chat(_ llm.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: l.content},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (l *streamLLM) ChatStream(_ llm.Context, _ llm.Request, cb llm.StreamCallback) (llm.Response, error) {
	words := strings.Fields(l.content)
	for i, w := range words {
		if i > 0 {
			cb(" ")
		}
		cb(w)
	}
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: l.content},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func TestStreamModeStreamsTokens(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: &streamLLM{content: "hello world"}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E4",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "hi",
	})

	// Streaming mode should NOT post a separate message
	for _, p := range messenger.posts {
		if p == "hello world" {
			t.Fatal("streamed answer should not be sent via PostMessage")
		}
	}
	// Should have markdown_text chunks
	var text strings.Builder
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "markdown_text" {
			text.WriteString(chunk["text"].(string))
		}
	}
	if got := text.String(); got != "hello world" {
		t.Fatalf("streamed text = %q, want %q", got, "hello world")
	}
	// Should have task_update complete
	foundComplete := false
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "task_update" && chunk["status"] == "complete" {
			foundComplete = true
			break
		}
	}
	if !foundComplete {
		t.Fatal("stream should mark task complete")
	}
}

func TestStreamModeFallsBackWhenAppendFails(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{streamTS: "200.000", appendErr: errors.New("append failed")}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: &streamLLM{content: "fallback answer"}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E5",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "hi",
	})

	if len(messenger.posts) == 0 {
		t.Fatal("expected fallback PostMessage when stream append fails")
	}
	got := messenger.posts[len(messenger.posts)-1]
	if !strings.Contains(got, "fallback answer") {
		t.Fatalf("fallback post = %q, want final answer", got)
	}
	if !strings.Contains(got, "streaming delivery failed") {
		t.Fatalf("fallback post = %q, want delivery note", got)
	}
}

func (l *replyLLM) Chat(_ llm.Context, _ llm.Request) (llm.Response, error) {
	content := l.content
	if content == "" {
		content = "ack"
	}
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: content},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}, nil
}

type replyLLM struct {
	content string
}

type fakeMessenger struct {
	posts     []string
	streamTS  string
	appendErr error
	chunks    []map[string]any
}

func (m *fakeMessenger) PostMessage(_ context.Context, _, _, text string) (string, error) {
	m.posts = append(m.posts, text)
	return "200.000", nil
}

func (m *fakeMessenger) StartStream(context.Context, string, string, string) (string, error) {
	if m.streamTS != "" {
		return m.streamTS, nil
	}
	return "", errors.New("stream unavailable")
}

func (m *fakeMessenger) AppendStream(_ context.Context, _, _ string, chunks []map[string]any) error {
	m.chunks = append(m.chunks, chunks...)
	return m.appendErr
}

func (m *fakeMessenger) StopStream(_ context.Context, _, _ string) error {
	return nil
}

func (m *fakeMessenger) DeleteMessage(context.Context, string, string) error {
	return nil
}

func (m *fakeMessenger) ThreadContext(context.Context, string, string, int) string {
	return ""
}
