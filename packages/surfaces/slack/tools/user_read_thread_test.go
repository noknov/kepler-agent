package slacktool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestUserReadThreadFormatsMessages(t *testing.T) {
	source := stubThreadReaderSource{client: slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/api/conversations.replies") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		return jsonResp(`{"ok":true,"messages":[{"user":"U1","text":"hello <@U2>","ts":"100.001"},{"user":"U2","text":"reply","ts":"100.002"}]}`), nil
	}))}
	result, err := (UserReadThreadTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"channel":"C123","thread_ts":"100.001","limit":10}`),
		Scope:     tool.Scope{UserID: "U123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "channel: C123") || !strings.Contains(result.Text(), "hello @U2") || !strings.Contains(result.Text(), "reply") {
		t.Fatalf("unexpected result: %q", result.Text())
	}
}

func TestUserReadThreadDefaultsToScope(t *testing.T) {
	var latest string
	source := stubThreadReaderSource{client: slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/api/conversations.history") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		latest = r.URL.Query().Get("latest")
		return jsonResp(`{"ok":true,"messages":[{"user":"U1","text":"scoped","ts":"1.0"}]}`), nil
	}))}
	_, err := (UserReadThreadTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{}`),
		Scope: tool.Scope{
			UserID: "U123",
			Values: map[string]string{"channel": "D9", "thread_ts": "9.9", "message_ts": "9.9"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest != "9.9" {
		t.Fatalf("latest = %q", latest)
	}
}

func TestUserReadThreadExplicitChannelIgnoresScope(t *testing.T) {
	var latest string
	source := stubThreadReaderSource{client: slack.NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		latest = r.URL.Query().Get("latest")
		return jsonResp(`{"ok":true,"messages":[{"user":"U1","text":"dm","ts":"1.0"}]}`), nil
	}))}
	_, err := (UserReadThreadTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"channel":"D0AJSE6PRLH"}`),
		Scope: tool.Scope{
			UserID: "U123",
			Values: map[string]string{"channel": "D_BOT", "thread_ts": "9.9", "message_ts": "9.9"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest != "" {
		t.Fatalf("latest = %q, want empty", latest)
	}
}

type stubThreadReaderSource struct {
	client *slack.Client
}

func (s stubThreadReaderSource) Client(context.Context, tool.Call) (*slack.Client, error) {
	return s.client, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
