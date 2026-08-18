package slack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

func TestUseConversationHistory(t *testing.T) {
	if !UseConversationHistory("9.9", "9.9") || UseConversationHistory("9.8", "9.9") {
		t.Fatal("expected flat conversation to use history")
	}
	if !UseConversationHistory("", "9.9") {
		t.Fatal("expected empty thread_ts to use history")
	}
}

func TestHistoryUsesLatestExclusive(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/conversations.history" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("latest"); got != "200.000" {
			t.Fatalf("latest = %q", got)
		}
		if got := r.URL.Query().Get("inclusive"); got != "false" {
			t.Fatalf("inclusive = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"messages":[{"user":"U1","text":"older","ts":"100.000"}]}`)),
		}, nil
	})}}
	messages, err := client.History(context.Background(), "D1", "200.000", 10)
	if err != nil || len(messages) != 1 || messages[0].Text != "older" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestThreadHistoryUsesConversationHistoryForFlatDM(t *testing.T) {
	called := ""
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = r.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true,"messages":[
				{"user":"U1","text":"newer","ts":"200.000"},
				{"user":"U1","text":"older","ts":"100.000"}
			]}`)),
		}, nil
	})}}
	history := client.ThreadHistory(context.Background(), "D1", "200.000", "200.000", 10)
	if called != "/api/conversations.history" {
		t.Fatalf("api = %q", called)
	}
	if len(history) != 2 || history[0].Text() != "Slack user U1: older" || history[1].Role != model.RoleUser {
		t.Fatalf("history=%#v", history)
	}
}
