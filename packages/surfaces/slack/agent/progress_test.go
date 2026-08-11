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

func TestProgressSummarizerUsesOnlySafeStructuredIntent(t *testing.T) {
	requests := make(chan model.Request, 1)
	summarizer := &ProgressSummarizer{Client: &progressModel{response: `{"action":"查询","target":"支付服务部署记录"}`, request: requests}, Model: "small"}
	text, err := summarizer.Summarize(context.Background(), "查一下支付服务部署记录", []model.ToolCall{{
		Name:      "notion-search",
		Arguments: json.RawMessage(`{"query":"支付服务部署记录","token":"secret","command":"dangerous"}`),
	}}, true)
	if err != nil || text != "查询支付服务部署记录" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	request := <-requests
	prompt := request.Messages[len(request.Messages)-1].Text()
	if !strings.Contains(prompt, "支付服务部署记录") || !strings.Contains(prompt, "notion-search") || strings.Contains(prompt, "secret") || strings.Contains(prompt, "dangerous") {
		t.Fatalf("unsafe progress prompt: %s", prompt)
	}
}

func TestToolStepProjectsSecondarySummaryWithoutPrimaryNarration(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "查一下支付服务"})
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
}

func TestStaleProgressSummaryCannotOverwriteNewLifecycle(t *testing.T) {
	messenger := &fakeMessenger{}
	stream := newSlackStream(context.Background(), messenger, slackconversation.Request{Channel: "C", ThreadTS: "T", Text: "diagnose"})
	stream.Start()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.mu.Lock()
	staleEpoch := stream.statusEpoch
	stream.mu.Unlock()
	stream.Lifecycle(transcript.Event{Type: transcript.ModelRequested})
	stream.setProgressStatus(staleEpoch, "is working", "Reading deployment records")

	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	for _, loading := range messenger.loading {
		if len(loading) > 0 && loading[0] == "Reading deployment records" {
			t.Fatalf("stale summary overwrote lifecycle status: %#v", messenger.loading)
		}
	}
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
