package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
	"github.com/wati/oncall-agent/internal/observability"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/session"
	"github.com/wati/oncall-agent/internal/slack"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
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

func TestProcessInjectsToolHealthSummary(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &captureLLM{}
	svc := NewService(
		store,
		&fakeMessenger{},
		agent.Runner{LLM: llmClient, Tools: registry.New(), MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)
	svc.HealthSummary = func() string {
		return "Tool health summary:\n- rag-search: degraded"
	}

	svc.HandleMention(ctx, Request{
		EventID:  "E-health",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "check code",
	})

	req := llmClient.LastRequest()
	found := false
	for _, msg := range req.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "rag-search: degraded") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("health summary not injected into LLM request: %#v", req.Messages)
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
	if !strings.Contains(got, "Something went wrong") || !strings.Contains(got, "Please try again later") {
		t.Fatalf("userFacingError() = %q, want generic error", got)
	}
	if strings.Contains(got, "err-test123") || strings.Contains(got, "Error ID") {
		t.Fatalf("userFacingError() should leave error id to the card title: %q", got)
	}
}

func TestStreamNoticeHasBlockBoundaries(t *testing.T) {
	got := streamNotice("上下文已压缩")
	if got != "\n\n_上下文已压缩_\n\n" {
		t.Fatalf("streamNotice() = %q", got)
	}
}

func TestCompleteTitleWithContext(t *testing.T) {
	got := completeTitleWithContext(agent.LocaleZH, "23,769 tokens (11%)")
	want := "传输完毕    ·    23,769 tokens (11%)"
	if got != want {
		t.Fatalf("completeTitleWithContext() = %q, want %q", got, want)
	}
}

func TestContextUsageTextHidesContextLimit(t *testing.T) {
	got := contextUsageText(200_000, 23_769)
	want := "23,769 tokens (11%)"
	if got != want {
		t.Fatalf("contextUsageText() = %q, want %q", got, want)
	}
	if strings.Contains(got, "/ 200,000") {
		t.Fatalf("contextUsageText() exposed context limit: %q", got)
	}
}

func TestFailedTitleIncludesErrorID(t *testing.T) {
	got := failedTitleWithErrorID(agent.LocaleZH, "err-test123")
	want := "链路中断 · err-test123"
	if got != want {
		t.Fatalf("failedTitleWithErrorID() = %q, want %q", got, want)
	}
	if fallback := failedTitleWithErrorID(agent.LocaleZH, ""); fallback != agent.FailedTitle(agent.LocaleZH) {
		t.Fatalf("failedTitleWithErrorID(empty) = %q, want failed title", fallback)
	}
}

func TestContextTokensFromStreamUsageUsesBaseWhenInputMissing(t *testing.T) {
	got := contextTokensFromStreamUsage(llm.Usage{CompletionTokens: 1200, TotalTokens: 1200}, 20_000)
	if got != 21_200 {
		t.Fatalf("contextTokensFromStreamUsage() = %d, want base plus completion", got)
	}
}

func TestContextTokensFromStreamUsageUsesInputWhenPresent(t *testing.T) {
	got := contextTokensFromStreamUsage(llm.Usage{
		PromptTokens:             18_000,
		CacheCreationInputTokens: 500,
		CacheReadInputTokens:     1_000,
		CompletionTokens:         200,
		TotalTokens:              19_700,
	}, 20_000)
	if got != 19_700 {
		t.Fatalf("contextTokensFromStreamUsage() = %d, want API input/cache/output sum", got)
	}
}

func TestTrimAndSummarizeReportsCompression(t *testing.T) {
	svc := NewService(
		nil,
		nil,
		agent.Runner{},
		memory.Builder{MaxMessages: 1, MaxSummaryChars: 10000},
		safety.PromptPolicy{},
		safety.Redactor{},
		nil,
	)
	turns := []memory.Turn{
		memory.UserTurn(strings.Repeat("old user context ", 400)),
		{Role: memory.RoleAssistant, Content: strings.Repeat("old assistant context ", 400)},
		memory.UserTurn("recent 1"),
		{Role: memory.RoleAssistant, Content: "recent 2"},
		memory.UserTurn("recent 3"),
		{Role: memory.RoleAssistant, Content: "recent 4"},
	}

	kept, summary, compressed := svc.trimAndSummarize(turns, "")

	if !compressed {
		t.Fatal("trimAndSummarize() compressed = false, want true")
	}
	if len(kept) >= len(turns) {
		t.Fatalf("trimAndSummarize() kept %d turns, want fewer than %d", len(kept), len(turns))
	}
	if !strings.Contains(summary, "Trimmed conversation summary") {
		t.Fatalf("summary does not describe trimmed context: %q", summary)
	}
}

func TestNewErrorID(t *testing.T) {
	got := newErrorID()
	if !strings.HasPrefix(got, "err-") || len(got) < 8 {
		t.Fatalf("newErrorID() = %q, want err-*", got)
	}
}

func TestStreamModePostsNonStreamingFormattedFinalAnswer(t *testing.T) {
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

	for _, chunk := range messenger.chunks {
		if chunk["type"] == "markdown_text" && strings.Contains(chunk["text"].(string), "Author:") {
			t.Fatalf("non-streaming final answer should not be replayed as markdown chunks: %#v", messenger.chunks)
		}
	}
	if !chunksContainText(messenger.chunks, "tokens (") {
		t.Fatalf("context usage not found in stream chunks: %#v", messenger.chunks)
	}
	if len(messenger.posts) != 1 {
		t.Fatalf("posts = %#v, want one final post", messenger.posts)
	}
	if got := messenger.posts[0]; got != "Author: <@U085SRJFCLX>" {
		t.Fatalf("posted final answer = %q, want formatted final answer", got)
	}
	// Progress stream should be marked complete
	foundComplete := false
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "task_update" && chunk["status"] == "complete" {
			foundComplete = true
		}
	}
	if !foundComplete {
		t.Fatalf("progress stream should be marked complete: %#v", messenger.chunks)
	}
}

func TestStreamModePostsNonStreamingFinalAnswer(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	final := strings.Repeat("流式传输", 40)
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: &replyLLM{content: final}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E3-long",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "stream it",
	})

	for _, chunk := range messenger.chunks {
		if chunk["type"] == "markdown_text" && strings.Contains(chunk["text"].(string), final) {
			t.Fatalf("non-streaming final answer should not be replayed as markdown chunks: %#v", messenger.chunks)
		}
	}
	if !chunksContainText(messenger.chunks, "tokens (") {
		t.Fatalf("context usage not found in stream chunks: %#v", messenger.chunks)
	}
	if len(messenger.posts) != 1 {
		t.Fatalf("posts = %#v, want one final post", messenger.posts)
	}
	if got := messenger.posts[0]; got != final {
		t.Fatalf("posted final answer length = %d, want %d", len([]rune(got)), len([]rune(final)))
	}
}

func TestCompressedContextNoticeOnlyAppearsInStream(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, session.Session{
		ID:       session.ID("C1", "100.000"),
		Channel:  "C1",
		ThreadTS: "100.000",
		UserID:   "U1",
		Turns: []memory.Turn{
			memory.UserTurn(strings.Repeat("旧上下文 ", 1200)),
			{Role: memory.RoleAssistant, Content: strings.Repeat("旧回答 ", 1200)},
			memory.UserTurn(strings.Repeat("更早上下文 ", 1200)),
			{Role: memory.RoleAssistant, Content: strings.Repeat("更早回答 ", 1200)},
			memory.UserTurn(strings.Repeat("最早上下文 ", 1200)),
		},
	}); err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: &replyLLM{content: "最终回答"}, MaxSteps: 1},
		memory.Builder{MaxMessages: 1, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E3-compressed",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "请回答",
	})

	if !chunksContainText(messenger.chunks, contextCompressingTitle(agent.LocaleZH)) {
		t.Fatalf("compressing context title not found in stream chunks: %#v", messenger.chunks)
	}
	if len(messenger.posts) != 1 {
		t.Fatalf("posts = %#v, want one final post", messenger.posts)
	}
	if strings.Contains(messenger.posts[0], contextCompressingTitle(agent.LocaleZH)) {
		t.Fatalf("posted final answer should not contain compressing context title: %q", messenger.posts[0])
	}
	if !taskUpdateOnStream(messenger.appends, messenger.streamTS, contextCompressingTitle(agent.LocaleZH)) {
		t.Fatalf("compressing context should appear as task_update on progress stream: %#v", messenger.chunks)
	}
	if taskUpdateCompleteOnStream(messenger.appends, messenger.streamTS, contextCompressingTitle(agent.LocaleZH)) {
		t.Fatalf("compressing context title should not be marked complete: %#v", messenger.chunks)
	}
	if !taskUpdateCompleteOnStream(messenger.appends, messenger.streamTS, agent.CompleteTitle(agent.LocaleZH)) {
		t.Fatalf("final complete title not found after compression: %#v", messenger.chunks)
	}
}

func TestCompressedContextNoticeStaysOffAnswerStream(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, session.Session{
		ID:       session.ID("C1", "100.000"),
		Channel:  "C1",
		ThreadTS: "100.000",
		UserID:   "U1",
		Turns: []memory.Turn{
			memory.UserTurn(strings.Repeat("旧上下文 ", 1200)),
			{Role: memory.RoleAssistant, Content: strings.Repeat("旧回答 ", 1200)},
			memory.UserTurn(strings.Repeat("更早上下文 ", 1200)),
			{Role: memory.RoleAssistant, Content: strings.Repeat("更早回答 ", 1200)},
			memory.UserTurn(strings.Repeat("最早上下文 ", 1200)),
		},
	}); err != nil {
		t.Fatal(err)
	}
	llmClient := &sequenceStreamLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "正在检索相关信息...",
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_1",
					Type: "function",
					Function: llm.ToolFunction{
						Name:      "echo",
						Arguments: `{"text":"ok"}`,
					},
				}},
			},
			Streamed: true,
		},
		{
			Message:  llm.Message{Role: "assistant", Content: "最终回答内容"},
			Streamed: true,
		},
	}}
	tools := registry.New()
	tools.Register(echoTool{})
	messenger := &fakeMessenger{streamSeq: []string{"progress.000", "answer.000"}}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 2, Capabilities: llm.Capabilities{NativeToolCalls: true}},
		memory.Builder{MaxMessages: 1, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E3-compressed-separate-streams",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "请回答",
	})

	compressTitle := contextCompressingTitle(agent.LocaleZH)
	progressChunks := chunksOnStream(messenger.appends, "progress.000")
	answerChunks := chunksOnStream(messenger.appends, "answer.000")

	if !taskUpdateOnStream(messenger.appends, "progress.000", compressTitle) {
		t.Fatalf("compressing title should be task_update on progress stream: %#v", progressChunks)
	}
	if chunksContainText(answerChunks, compressTitle) {
		t.Fatalf("compressing title should not appear on answer stream: %#v", answerChunks)
	}
	for _, chunk := range answerChunks {
		if chunk["type"] == "markdown_text" && strings.Contains(chunk["text"].(string), compressTitle) {
			t.Fatalf("answer stream markdown should not contain compressing title: %#v", chunk)
		}
	}
}

func TestActiveReplySteeringShowsSeparatedStatusInChinese(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
	tools := registry.New()
	tools.Register(tool)
	llmClient := &toolThenFinalLLM{}
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 3},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.HandleMention(ctx, Request{
			EventID:  "E7-zh-steer",
			UserID:   "U1",
			Channel:  "C1",
			ThreadTS: "100.000",
			Text:     "查一下生产日志",
		})
	}()

	<-tool.started
	handled := svc.HandleReply(ctx, Request{
		EventID:  "E8-zh-steer",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "改成查 staging",
	})
	if !handled {
		t.Fatal("active reply was not handled")
	}
	close(tool.release)
	<-done

	cardTitle := agent.SteeringQueuedTitle(agent.LocaleZH)
	bodyText := steeringAppliedMessage(agent.LocaleZH)
	foundCard := false
	foundBody := false
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "task_update" && strings.HasPrefix(chunk["title"].(string), cardTitle) {
			foundCard = true
		}
		if chunk["type"] == "markdown_text" && chunk["text"] == bodyText {
			foundBody = true
		}
		if chunk["type"] == "markdown_text" && strings.HasSuffix(chunk["text"].(string), "\n\n") {
			t.Fatalf("steering markdown should not end with blank line: %q", chunk["text"])
		}
	}
	if !foundCard {
		t.Fatalf("steering card title %q not found: %#v", cardTitle, messenger.chunks)
	}
	if !foundBody {
		t.Fatalf("steering body text %q not found: %#v", bodyText, messenger.chunks)
	}
}

type streamLLM struct {
	content     string
	chatCalls   int
	streamCalls int
}

func (l *streamLLM) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	l.chatCalls++
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: l.content},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (l *streamLLM) ChatStream(_ context.Context, _ llm.Request, h llm.StreamHandler) (llm.Response, error) {
	l.streamCalls++
	words := strings.Fields(l.content)
	for i, w := range words {
		if i > 0 && h.OnText != nil {
			h.OnText(" ")
		}
		if h.OnText != nil {
			h.OnText(w)
		}
	}
	return llm.Response{
		Message:  llm.Message{Role: "assistant", Content: l.content},
		Usage:    llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		Streamed: true,
	}, nil
}

type sequenceStreamLLM struct {
	responses   []llm.Response
	streamCalls int
}

func (l *sequenceStreamLLM) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	if len(l.responses) == 0 {
		return llm.Response{}, errors.New("unexpected chat call")
	}
	resp := l.responses[0]
	l.responses = l.responses[1:]
	return resp, nil
}

func (l *sequenceStreamLLM) ChatStream(_ context.Context, _ llm.Request, h llm.StreamHandler) (llm.Response, error) {
	l.streamCalls++
	if len(l.responses) == 0 {
		return llm.Response{}, errors.New("unexpected stream call")
	}
	resp := l.responses[0]
	l.responses = l.responses[1:]
	if resp.Streamed && resp.Message.Content != "" {
		if h.OnText != nil {
			h.OnText(resp.Message.Content)
		}
	}
	if len(resp.Message.ToolCalls) > 0 && h.OnToolCallsStarted != nil {
		h.OnToolCallsStarted()
	}
	return resp, nil
}

type streamingToolLLM struct {
	responses []llm.Response
}

func (l *streamingToolLLM) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	if len(l.responses) == 0 {
		return llm.Response{}, errors.New("unexpected chat call")
	}
	resp := l.responses[0]
	l.responses = l.responses[1:]
	return resp, nil
}

func (l *streamingToolLLM) ChatStream(_ context.Context, _ llm.Request, h llm.StreamHandler) (llm.Response, error) {
	if len(l.responses) == 0 {
		return llm.Response{}, errors.New("unexpected stream call")
	}
	resp := l.responses[0]
	l.responses = l.responses[1:]
	if strings.TrimSpace(resp.Message.Content) != "" && h.OnText != nil {
		h.OnText(resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) > 0 && h.OnToolCallsStarted != nil {
		h.OnToolCallsStarted()
	}
	return resp, nil
}

func TestStreamModeUsesNativeStreamWhenToolsAreOmitted(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &streamLLM{content: "hello world"}
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: registry.New(), MaxSteps: 3},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E4-native",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "hi",
	})

	if llmClient.streamCalls != 1 {
		t.Fatalf("ChatStream calls = %d, want 1", llmClient.streamCalls)
	}
	if llmClient.chatCalls != 0 {
		t.Fatalf("Chat calls = %d, want 0", llmClient.chatCalls)
	}
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
	gotText := text.String()
	if !strings.Contains(gotText, "hello world") {
		t.Fatalf("streamed text = %q, want final answer", gotText)
	}
	if strings.Contains(gotText, "cancel") {
		t.Fatalf("streamed text = %q, should not include active control hint", gotText)
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

func TestToolNarrationAndFinalAnswerUseSeparateStreams(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &sequenceStreamLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "让我看看 wati-voice-service 的结构...",
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_1",
					Type: "function",
					Function: llm.ToolFunction{
						Name:      "echo",
						Arguments: `{"text":"ok"}`,
					},
				}},
			},
			Streamed: true,
		},
		{
			Message:  llm.Message{Role: "assistant", Content: "## :telephone_receiver: wati-voice-service"},
			Streamed: true,
		},
	}}
	tools := registry.New()
	tools.Register(echoTool{})
	messenger := &fakeMessenger{streamSeq: []string{"progress.000", "answer.000"}}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 2, Capabilities: llm.Capabilities{NativeToolCalls: true}},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E4-separate-streams",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "看一下 voice service",
	})

	var narrationTS, answerTS string
	for _, append := range messenger.appends {
		for _, chunk := range append.chunks {
			if chunk["type"] != "markdown_text" {
				continue
			}
			text := chunk["text"].(string)
			if strings.Contains(text, "让我看看") {
				narrationTS = append.ts
			}
			if strings.Contains(text, "wati-voice-service") && strings.Contains(text, "##") {
				answerTS = append.ts
			}
		}
	}
	if narrationTS != "progress.000" {
		t.Fatalf("narration ts = %q, want progress stream", narrationTS)
	}
	if answerTS != "answer.000" {
		t.Fatalf("answer ts = %q, want answer stream", answerTS)
	}
}

func TestToolNarrationAndFinalAnswerSeparateWithDefaultMaxSteps(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &sequenceStreamLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "让我看看 wati-voice-service 的结构...",
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_1",
					Type: "function",
					Function: llm.ToolFunction{
						Name:      "echo",
						Arguments: `{"text":"ok"}`,
					},
				}},
			},
			Streamed: true,
		},
		{
			Message:  llm.Message{Role: "assistant", Content: "## :telephone_receiver: wati-voice-service"},
			Streamed: true,
		},
	}}
	tools := registry.New()
	tools.Register(echoTool{})
	messenger := &fakeMessenger{streamSeq: []string{"progress.000", "answer.000"}}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 12, Capabilities: llm.Capabilities{NativeToolCalls: true}},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E4-default-max-steps",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "看一下 voice service",
	})

	var narrationTS, answerTS string
	for _, append := range messenger.appends {
		for _, chunk := range append.chunks {
			if chunk["type"] != "markdown_text" {
				continue
			}
			text := chunk["text"].(string)
			if strings.Contains(text, "让我看看") {
				narrationTS = append.ts
			}
			if strings.Contains(text, "wati-voice-service") && strings.Contains(text, "##") {
				answerTS = append.ts
			}
		}
	}
	if narrationTS != "progress.000" {
		t.Fatalf("narration ts = %q, want progress stream", narrationTS)
	}
	if answerTS != "answer.000" {
		t.Fatalf("answer ts = %q, want answer stream", answerTS)
	}
}

func TestMultiRoundToolNarrationStaysOnProgressStream(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &streamingToolLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "让我先看看这个 PR 的内容...",
				ToolCalls: []llm.ToolCall{{
					ID: "tool_1", Type: "function",
					Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"ok"}`},
				}},
			},
		},
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "让我看看仓库里现有的 Internal Controller...",
				ToolCalls: []llm.ToolCall{{
					ID: "tool_2", Type: "function",
					Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"ok2"}`},
				}},
			},
		},
	}}
	tools := registry.New()
	tools.Register(echoTool{})
	messenger := &fakeMessenger{streamSeq: []string{"progress.000", "answer.000", "extra.000"}}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 12, Capabilities: llm.Capabilities{NativeToolCalls: true}},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E4-multi-tool",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "review this PR",
	})

	var answerMarkdown bool
	for _, append := range messenger.appends {
		if append.ts != "progress.000" {
			for _, chunk := range append.chunks {
				if chunk["type"] == "markdown_text" {
					answerMarkdown = true
				}
			}
		}
	}
	if answerMarkdown {
		t.Fatal("second tool round should not open the answer stream")
	}
}

func TestStreamedToolNarrationAndFinalAnswerUseSeparateStreams(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &streamingToolLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "让我看看 wati-voice-service 的结构...",
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_1",
					Type: "function",
					Function: llm.ToolFunction{
						Name:      "echo",
						Arguments: `{"text":"ok"}`,
					},
				}},
			},
			Streamed: true,
		},
		{
			Message:  llm.Message{Role: "assistant", Content: "## :telephone_receiver: wati-voice-service"},
			Streamed: true,
		},
	}}
	tools := registry.New()
	tools.Register(echoTool{})
	messenger := &fakeMessenger{streamSeq: []string{"progress.000", "answer.000"}}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 2, Capabilities: llm.Capabilities{NativeToolCalls: true}},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E4-streamed-separate",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "看一下 voice service",
	})

	var narrationTS, answerTS string
	for _, append := range messenger.appends {
		for _, chunk := range append.chunks {
			if chunk["type"] != "markdown_text" {
				continue
			}
			text := chunk["text"].(string)
			if strings.Contains(text, "让我看看") {
				narrationTS = append.ts
			}
			if strings.Contains(text, "wati-voice-service") && strings.Contains(text, "##") {
				answerTS = append.ts
			}
		}
	}
	if narrationTS != "progress.000" {
		t.Fatalf("narration ts = %q, want progress stream", narrationTS)
	}
	if answerTS != "answer.000" {
		t.Fatalf("answer ts = %q, want answer stream", answerTS)
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

func TestStreamStatusFailureDoesNotAffectFinalAnswer(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{streamTS: "200.000", statusErr: errors.New("status failed")}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: &replyLLM{content: "final answer"}, MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	svc.HandleMention(ctx, Request{
		EventID:  "E6",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "hi",
	})

	for _, chunk := range messenger.chunks {
		if chunk["type"] == "markdown_text" && strings.Contains(chunk["text"].(string), "final answer") {
			t.Fatalf("non-streaming final answer should not be replayed as markdown chunks: %#v", messenger.chunks)
		}
	}
	if !chunksContainText(messenger.chunks, "tokens (") {
		t.Fatalf("context usage not found in stream chunks: %#v", messenger.chunks)
	}
	if len(messenger.posts) != 1 || messenger.posts[0] != "final answer" {
		t.Fatalf("posts = %#v, want final answer post", messenger.posts)
	}
	for _, p := range messenger.posts {
		if strings.Contains(p, "streaming delivery failed") {
			t.Fatal("status-only failure should not trigger delivery-failed prefix")
		}
	}
}

func TestActiveReplyIsInjectedIntoNextStep(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
	tools := registry.New()
	tools.Register(tool)
	llmClient := &toolThenFinalLLM{}
	svc := NewService(
		store,
		&fakeMessenger{streamTS: "200.000"},
		agent.Runner{LLM: llmClient, Tools: tools, MaxSteps: 3},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.HandleMention(ctx, Request{
			EventID:  "E7",
			UserID:   "U1",
			Channel:  "C1",
			ThreadTS: "100.000",
			Text:     "check prod logs",
		})
	}()

	<-tool.started
	handled := svc.HandleReply(ctx, Request{
		EventID:  "E8",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "actually use staging",
	})
	if !handled {
		t.Fatal("active reply was not handled")
	}
	close(tool.release)
	<-done

	requests := llmClient.Requests()
	if len(requests) < 2 {
		t.Fatalf("LLM requests = %d, want at least 2", len(requests))
	}
	found := false
	for _, msg := range requests[1].Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "actually use staging") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("queued guidance not injected into next step: %#v", requests[1].Messages)
	}
	foundStatus := false
	foundText := false
	for _, chunk := range svc.Messenger.(*fakeMessenger).chunks {
		if chunk["type"] == "task_update" && strings.HasPrefix(chunk["title"].(string), "Conversation guided") {
			foundStatus = true
		}
		if chunk["type"] == "markdown_text" && chunk["text"] == "\n\n_Conversation guided_\n" {
			foundText = true
		}
	}
	if !foundStatus {
		t.Fatalf("steering status not found: %#v", svc.Messenger.(*fakeMessenger).chunks)
	}
	if !foundText {
		t.Fatalf("steering stream text not found: %#v", svc.Messenger.(*fakeMessenger).chunks)
	}
}

func TestActiveReplyCanCancelRun(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llmClient := &cancelAwareLLM{started: make(chan struct{})}
	messenger := &fakeMessenger{streamTS: "200.000"}
	svc := NewService(
		store,
		messenger,
		agent.Runner{LLM: llmClient, Tools: registry.New(), MaxSteps: 1},
		memory.Builder{MaxMessages: 10, MaxToolChars: 1000, MaxThreadChars: 1000, MaxSummaryChars: 1000},
		safety.PromptPolicy{},
		safety.Redactor{},
		observability.NewRecorder(),
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.HandleMention(ctx, Request{
			EventID:  "E9",
			UserID:   "U1",
			Channel:  "C1",
			ThreadTS: "100.000",
			Text:     "keep working",
		})
	}()

	<-llmClient.started
	handled := svc.HandleReply(ctx, Request{
		EventID:  "E10",
		UserID:   "U1",
		Channel:  "C1",
		ThreadTS: "100.000",
		Text:     "cancel",
	})
	if !handled {
		t.Fatal("cancel reply was not handled")
	}
	<-done

	var text strings.Builder
	for _, chunk := range messenger.chunks {
		if chunk["type"] == "task_update" {
			text.WriteString(chunk["title"].(string))
			text.WriteString("\n")
		}
		if chunk["type"] == "markdown_text" {
			text.WriteString(chunk["text"].(string))
			text.WriteString("\n")
		}
	}
	got := text.String()
	if !strings.Contains(got, "Cancelled") {
		t.Fatalf("cancel status not found in stream chunks: %q", got)
	}
	if strings.Contains(got, "Cancelled this request") {
		t.Fatalf("cancel should not emit redundant body text: %q", got)
	}
	if strings.Contains(got, "Something went wrong") {
		t.Fatalf("cancel should not emit generic error: %q", got)
	}
}

func (l *replyLLM) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
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

type captureLLM struct {
	mu      sync.Mutex
	request llm.Request
}

func (l *captureLLM) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	l.mu.Lock()
	l.request = req
	l.mu.Unlock()
	return llm.Response{
		Message: llm.Message{Role: "assistant", Content: "ack"},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}, nil
}

func (l *captureLLM) LastRequest() llm.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.request
}

type cancelAwareLLM struct {
	started chan struct{}
	once    sync.Once
}

func (l *cancelAwareLLM) Chat(ctx context.Context, _ llm.Request) (llm.Response, error) {
	l.once.Do(func() { close(l.started) })
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

type toolThenFinalLLM struct {
	mu       sync.Mutex
	requests []llm.Request
	calls    int
}

func (l *toolThenFinalLLM) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, req)
	l.calls++
	if l.calls == 1 {
		return llm.Response{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   "tool_1",
				Type: "function",
				Function: llm.ToolFunction{
					Name:      "block",
					Arguments: `{}`,
				},
			}},
		}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func (l *toolThenFinalLLM) Requests() []llm.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]llm.Request(nil), l.requests...)
}

type blockingTool struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (t *blockingTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "block",
			Description: "block until released",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (t *blockingTool) Execute(ctx context.Context, _ json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	t.once.Do(func() { close(t.started) })
	select {
	case <-t.release:
		return registry.Result{Content: "released"}, nil
	case <-ctx.Done():
		return registry.Result{}, ctx.Err()
	}
}

type echoTool struct{}

func (echoTool) Parallel() bool { return true }

func (echoTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "echo",
			Description: "echo input",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (echoTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: string(args)}, nil
}

type fakeMessenger struct {
	posts      []string
	streamTS   string
	streamSeq  []string
	startDelay time.Duration
	appendErr  error
	statusErr  error
	chunks     []map[string]any
	appends    []streamAppend
}

type streamAppend struct {
	ts     string
	chunks []map[string]any
}

func chunksContainText(chunks []map[string]any, want string) bool {
	for _, chunk := range chunks {
		var values []string
		if text, ok := chunk["text"].(string); ok {
			values = append(values, text)
		}
		if title, ok := chunk["title"].(string); ok {
			values = append(values, title)
		}
		for _, value := range values {
			if strings.Contains(value, want) {
				return true
			}
		}
	}
	return false
}

func chunksOnStream(appends []streamAppend, ts string) []map[string]any {
	var out []map[string]any
	for _, item := range appends {
		if item.ts == ts {
			out = append(out, item.chunks...)
		}
	}
	return out
}

func taskUpdateOnStream(appends []streamAppend, ts, titleWant string) bool {
	for _, item := range appends {
		if item.ts != ts {
			continue
		}
		for _, chunk := range item.chunks {
			if chunk["type"] != "task_update" {
				continue
			}
			title, _ := chunk["title"].(string)
			if strings.Contains(title, titleWant) {
				return true
			}
		}
	}
	return false
}

func taskUpdateCompleteOnStream(appends []streamAppend, ts, titleWant string) bool {
	for _, item := range appends {
		if item.ts != ts {
			continue
		}
		for _, chunk := range item.chunks {
			if chunk["type"] != "task_update" || chunk["status"] != "complete" {
				continue
			}
			title, _ := chunk["title"].(string)
			if strings.Contains(title, titleWant) {
				return true
			}
		}
	}
	return false
}

func (m *fakeMessenger) PostMessage(_ context.Context, _, _, text string) (string, error) {
	m.posts = append(m.posts, text)
	return "200.000", nil
}

func (m *fakeMessenger) StartStream(context.Context, string, string, string) (string, error) {
	if m.startDelay > 0 {
		time.Sleep(m.startDelay)
	}
	if len(m.streamSeq) > 0 {
		ts := m.streamSeq[0]
		m.streamSeq = m.streamSeq[1:]
		return ts, nil
	}
	if m.streamTS != "" {
		return m.streamTS, nil
	}
	return "", errors.New("stream unavailable")
}

func (m *fakeMessenger) AppendStream(_ context.Context, _, ts string, chunks []map[string]any) error {
	m.appends = append(m.appends, streamAppend{ts: ts, chunks: chunks})
	m.chunks = append(m.chunks, chunks...)
	if m.statusErr != nil && isOnlyTaskUpdate(chunks) {
		return m.statusErr
	}
	return m.appendErr
}

func isOnlyTaskUpdate(chunks []map[string]any) bool {
	if len(chunks) != 1 {
		return false
	}
	return chunks[0]["type"] == "task_update"
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
