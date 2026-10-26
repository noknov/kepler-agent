package slackhandler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/observability"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type suggestionTransport func(*http.Request) (*http.Response, error)

func (f suggestionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMessagesTabOffersContextualPromptsWithoutExecutingTask(t *testing.T) {
	conv := &triggerConversation{}
	var payloads []map[string]any
	client := slack.NewTestClient(suggestionTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/assistant.threads.setSuggestedPrompts" {
			t.Fatal("sent a message or unexpected API", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	}))
	h := Handler{Slack: client, Conv: conv, Access: safety.NewAccessPolicy([]string{"U1"}, nil)}
	h.Cfg.Slack.DefaultLocale = "zh-CN"
	event := slack.Event{Type: "app_home_opened", Tab: "messages", TeamID: "T1", User: "U1", Channel: "D1", Context: slack.AppContext{Entities: []slack.ContextEntity{{Type: "slack#/types/channel_id", Value: "C123", TeamID: "T1"}}}}
	if err := h.Handle(context.Background(), "home", event); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || len(conv.requests) != 0 {
		t.Fatalf("calls=%d tasks=%d", len(payloads), len(conv.requests))
	}
	body := payloads[0]
	entries := body["prompts"].([]any)
	if len(entries) != 4 || body["thread_ts"] != nil || body["channel_id"] != "D1" || !strings.Contains(entries[0].(map[string]any)["message"].(string), "<#C123>") || !strings.Contains(body["title"].(string), "处理") {
		t.Fatal(body)
	}
	event.Context = slack.AppContext{}
	if err := h.Handle(context.Background(), "empty-context", event); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payloads[1])
	if strings.Contains(string(raw), "C123") || len(payloads[1]["prompts"].([]any)) != 3 {
		t.Fatal("retained old context", string(raw))
	}
	for _, blocked := range []slack.Event{{Type: "app_home_opened", Tab: "messages", User: "U2", Channel: "D1"}, {Type: "app_home_opened", Tab: "messages", User: "U1", Channel: "C1"}, {Type: "app_home_opened", Tab: "messages", Channel: "D1"}} {
		if err := h.Handle(context.Background(), "blocked", blocked); err != nil {
			t.Fatal(err)
		}
	}
	if len(payloads) != 2 {
		t.Fatal("published prompts for disallowed/invalid user or destination")
	}
}

func TestOptionalSuggestedPromptFailureDoesNotFailIngress(t *testing.T) {
	recorder := observability.NewRecorder()
	client := slack.NewTestClient(suggestionTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":"static_prompts_configured"}`))}, nil
	}))
	h := Handler{Slack: client, Metrics: recorder}
	if err := h.Handle(context.Background(), "home", slack.Event{Type: "app_home_opened", Tab: "messages", User: "U1", Channel: "D1"}); err != nil {
		t.Fatal(err)
	}
	if recorder.Snapshot().AgentEvents["slack_suggested_prompts_failed"] != 1 {
		t.Fatal("missing failure visibility")
	}
}
