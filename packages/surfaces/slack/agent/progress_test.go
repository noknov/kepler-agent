package slackagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

type progressModel struct {
	response string
	request  chan model.Request
}

func (m *progressModel) Generate(ctx context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	if m.request != nil {
		m.request <- request
	}
	return model.Response{Message: model.TextMessage(model.RoleAssistant, m.response), FinishReason: model.FinishStop}, nil
}

type blockingProgressModel struct {
	requests chan model.Request
	release  chan struct{}
}

func (m *blockingProgressModel) Generate(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
	m.requests <- request
	<-m.release
	return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"action":"Reading","target":"records"}`), FinishReason: model.FinishStop}, nil
}

func TestProgressSummarizerUsesOnlySafeStructuredIntent(t *testing.T) {
	requests := make(chan model.Request, 1)
	summarizer := &ProgressSummarizer{
		Client: &progressModel{response: `{"action":"查询","target":"支付服务部署记录"}`, request: requests},
		Model:  "small",
		Sanitize: func(text string) string {
			return strings.ReplaceAll(text, "secret", "[redacted]")
		},
	}
	text, err := summarizer.Summarize(context.Background(), "查一下支付服务部署记录", []model.ToolCall{{
		Name:      "notion-search",
		Arguments: json.RawMessage(`{"query":"支付服务部署记录","token":"secret","command":"dangerous"}`),
	}}, true)
	if err != nil || text != "查询支付服务部署记录" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	request := <-requests
	system := request.Messages[0].Text()
	if !strings.Contains(system, "Slack loading") || !strings.Contains(system, "参数值") {
		t.Fatalf("progress system prompt is not a general user-facing contract: %s", system)
	}
	prompt := request.Messages[len(request.Messages)-1].Text()
	if !strings.Contains(prompt, "支付服务部署记录") || !strings.Contains(prompt, "dangerous") || strings.Contains(prompt, "notion-search") || strings.Contains(prompt, "secret") {
		t.Fatalf("unsafe progress prompt: %s", prompt)
	}
}

func TestProgressSummarizerIncludesToolDescriptionAsOperationSemantics(t *testing.T) {
	requests := make(chan model.Request, 1)
	summarizer := &ProgressSummarizer{
		Client: &progressModel{response: `{"action":"Fetching","target":"PR diff"}`, request: requests},
		ToolDescriptions: map[string]string{
			"github-pr_diff": "Fetch GitHub pull request metadata and unified diff for review or investigation.",
		},
	}
	_, err := summarizer.Summarize(context.Background(), "review PR #123", []model.ToolCall{{
		Name:      "github-pr_diff",
		Arguments: json.RawMessage(`{"repository":"owner/repo","pr":123}`),
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	request := <-requests
	system := request.Messages[0].Text()
	if !strings.Contains(system, "Do not restate the user's task") || !strings.Contains(system, "operation description") {
		t.Fatalf("progress prompt does not prevent task restatement: %s", system)
	}
	prompt := request.Messages[len(request.Messages)-1].Text()
	if !strings.Contains(prompt, "Fetch GitHub pull request metadata and unified diff") || !strings.Contains(prompt, `"pr":123`) || strings.Contains(prompt, "github-pr_diff") {
		t.Fatalf("progress prompt omitted operation semantics: %s", prompt)
	}
}

func TestToolStepProjectsSecondarySummaryWithoutPrimaryNarration(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "查一下支付服务", Locale: "zh-CN"})
	stream.progress = &ProgressSummarizer{Client: &progressModel{response: `{"action":"查询","target":"支付服务部署记录"}`}}
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.CommitStep(model.Message{Role: model.RoleAssistant, Content: []model.Content{
		{Type: model.ContentText, Text: "internal model narration"},
		{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "notion-search", Arguments: json.RawMessage(`{"query":"支付服务部署记录"}`)}},
	}})

	deadline := time.Now().Add(time.Second)
	for {
		messenger.mu.Lock()
		found := len(messenger.loading) >= 2
		messenger.mu.Unlock()
		if found || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.loading; len(got) != 2 || len(got[1]) != 1 || got[1][0] != "查询支付服务部署记录" {
		t.Fatalf("loading messages=%#v", got)
	}
	if got := messenger.statuses[1]; got != "正在思考" {
		t.Fatalf("progress status = %q, want 正在思考", got)
	}
}

func TestStreamedToolCallProjectsProgressSummary(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "search repo"})
	stream.progress = &ProgressSummarizer{Client: &progressModel{response: `{"action":"Searching","target":"repository matches"}`}}
	stream.Start()
	router := &eventRouter{streams: map[string]*slackStream{"turn": stream}}
	router.Publish(context.Background(), transcript.Event{TurnID: "turn", Type: transcript.ModelRequested})
	router.Publish(context.Background(), transcript.Event{TurnID: "turn", Type: transcript.ModelStreamed, Model: &model.StreamEvent{
		Type: model.StreamToolCallDone,
		ToolCall: &model.ToolCall{
			ID:        "call",
			Name:      "code-search",
			Arguments: json.RawMessage(`{"query":"repo","source":"working_tree"}`),
		},
	}})

	deadline := time.Now().Add(time.Second)
	for {
		messenger.mu.Lock()
		found := len(messenger.loading) >= 2
		messenger.mu.Unlock()
		if found || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.statuses; len(got) != 2 || got[0] != "is thinking" || got[1] != "is thinking" {
		t.Fatalf("statuses=%#v", got)
	}
	if got := messenger.loading; len(got) != 2 || len(got[1]) != 1 || got[1][0] != "Searching repository matches" {
		t.Fatalf("loading messages=%#v", got)
	}
}

func TestThinkingLifecycleDoesNotDowngradeProgressLoading(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "search repo"})
	stream.progress = &ProgressSummarizer{Client: &progressModel{response: `{"action":"Searching","target":"repository matches"}`}}
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.ToolStep([]model.ToolCall{{ID: "call", Name: "code-search"}})

	deadline := time.Now().Add(time.Second)
	for {
		messenger.mu.Lock()
		found := len(messenger.loading) >= 2
		messenger.mu.Unlock()
		if found || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.statuses; len(got) != 2 || got[0] != "is thinking" || got[1] != "is thinking" {
		t.Fatalf("statuses=%#v", got)
	}
	if got := messenger.loading; len(got) != 2 || len(got[1]) != 1 || got[1][0] != "Searching repository matches" {
		t.Fatalf("loading messages=%#v", got)
	}
}

func TestToolStepDeduplicatesStreamedAndFinalAssistantCalls(t *testing.T) {
	requests := make(chan model.Request, 2)
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "search repo"})
	stream.progress = &ProgressSummarizer{Client: &progressModel{response: `{"action":"Searching","target":"repository matches"}`, request: requests}}
	stream.Start()
	call := model.ToolCall{ID: "call", Name: "code-search", Arguments: json.RawMessage(`{"query":"repo","source":"working_tree"}`)}

	stream.ToolStep([]model.ToolCall{call})
	<-requests
	stream.CommitStep(model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}})

	select {
	case <-requests:
		t.Fatal("duplicate tool call triggered a second progress summary")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestToolStepSerializesProgressSummaries(t *testing.T) {
	modelClient := &blockingProgressModel{requests: make(chan model.Request, 2), release: make(chan struct{})}
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "inspect repo"})
	stream.progress = &ProgressSummarizer{Client: modelClient, ToolDescriptions: map[string]string{
		"repo-read": "Read repository files.", "repo-search": "Search repository files.",
	}}
	stream.Start()
	stream.ToolStep([]model.ToolCall{{ID: "one", Name: "repo-read"}})
	first := <-modelClient.requests
	stream.ToolStep([]model.ToolCall{{ID: "two", Name: "repo-search"}})
	select {
	case <-modelClient.requests:
		t.Fatal("started a concurrent progress summary")
	case <-time.After(50 * time.Millisecond):
	}
	close(modelClient.release)
	second := <-modelClient.requests
	if first.Messages[1].Text() == second.Messages[1].Text() || !strings.Contains(second.Messages[1].Text(), "Search repository files") {
		t.Fatalf("queued progress input = %q, want second operation", second.Messages[1].Text())
	}
}

func TestStaleProgressSummaryCannotOverwriteNewLifecycle(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "diagnose"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.mu.Lock()
	staleEpoch := stream.statusEpoch
	stream.mu.Unlock()
	stream.Lifecycle(transcript.Event{Type: transcript.ApprovalRequested})
	stream.setProgressStatus(staleEpoch, "is thinking", "Reading deployment records")

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	for _, loading := range messenger.loading {
		if len(loading) > 0 && loading[0] == "Reading deployment records" {
			t.Fatalf("stale summary overwrote lifecycle status: %#v", messenger.loading)
		}
	}
}

func TestRestoreThreadStatusAfterStreamDelivery(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), &streamingStatusMessenger{
		fake: messenger,
	}, slackconversation.Request{Channel: "C1", ThreadTS: "T1", EventID: "Ev1"})
	stream.Start()
	stream.mu.Lock()
	stream.statusEpoch = 1
	stream.mu.Unlock()
	stream.setProgressStatus(1, "is thinking", "Searching repository matches")
	stream.flushStreamUpdate("partial answer", false)

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if got := messenger.loading; len(got) < 2 || len(got[len(got)-1]) != 1 || got[len(got)-1][0] != "Searching repository matches" {
		t.Fatalf("loading messages=%#v", got)
	}
}

type streamingStatusMessenger struct {
	fake *fakeMessenger
}

func (m *streamingStatusMessenger) PostMessage(context.Context, string, string, string) (string, error) {
	return "1.0", nil
}
func (m *streamingStatusMessenger) PostMarkdownMessage(context.Context, string, string, string) (string, error) {
	return "1.0", nil
}
func (m *streamingStatusMessenger) PostMarkdownMessageWithID(_ context.Context, _, _, _ string, _ string) (string, error) {
	return "1.0", nil
}
func (m *streamingStatusMessenger) StartStream(context.Context, string, string, string) (string, error) {
	return "1.0", nil
}
func (m *streamingStatusMessenger) AppendStream(context.Context, string, string, []map[string]any) error {
	return nil
}
func (m *streamingStatusMessenger) StopStream(context.Context, string, string) error {
	return nil
}
func (m *streamingStatusMessenger) SetThreadStatus(ctx context.Context, channel, threadTS, status string, loading []string) error {
	return m.fake.SetThreadStatus(ctx, channel, threadTS, status, loading)
}

func TestProgressRequiresStructuredResponse(t *testing.T) {
	for _, text := range []string{
		"unstructured response",
		`{"status":"查询支付服务"}`,
		`{"action":"查询","target":"支付服务"} trailing`,
	} {
		if got := decodeProgress(text, true); got != "" {
			t.Fatalf("decodeProgress(%q) = %q", text, got)
		}
	}
	if got := decodeProgress(`{"action":"Reading","target":"deployment records"}`, false); got != "Reading deployment records" {
		t.Fatalf("valid structured response = %q", got)
	}
}
