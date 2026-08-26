package slackfiles

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

func TestThreadHistoryUsesConversationHistoryForFlatDM(t *testing.T) {
	called := ""
	slackClient := slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = r.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true,"messages":[
				{"user":"U1","text":"newer","ts":"200.000"},
				{"user":"U1","text":"older","ts":"100.000"}
			]}`)),
		}, nil
	}))
	history, err := ThreadHistory(context.Background(), slackClient, "D1", "200.000", "200.000", 10)
	if err != nil {
		t.Fatal(err)
	}
	if called != "/api/conversations.history" {
		t.Fatalf("api = %q", called)
	}
	if len(history) != 2 || history[0].Text() != "Slack user U1: older" || history[1].Role != model.RoleUser {
		t.Fatalf("history=%#v", history)
	}
}

func TestThreadHistoryReturnsConversationReadError(t *testing.T) {
	slackClient := slack.NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	}))
	if _, err := ThreadHistory(context.Background(), slackClient, "C1", "100.000", "200.000", 10); err == nil {
		t.Fatal("expected history read error")
	}
}

func TestHistoryMessageIncludesImages(t *testing.T) {
	downloader := &imageDownloader{}
	message, ok := HistoryMessage(context.Background(), downloader, slack.Message{
		User:  "U1",
		Text:  "see this",
		Files: []slack.File{{ID: "F1", Mimetype: "image/png"}},
	}, "B1", ThreadImageBudget())
	if !ok {
		t.Fatal("expected history message")
	}
	hasImage := false
	for _, block := range message.Content {
		if block.Type == model.ContentImage {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("expected image content: %#v", message.Content)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
