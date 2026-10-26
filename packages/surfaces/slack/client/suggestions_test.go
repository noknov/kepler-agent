package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestSuggestedPromptsUseAgentViewWireContract(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/assistant.threads.setSuggestedPrompts" {
			t.Fatal(r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["channel_id"]) != `"D123"` || string(body["title"]) != `"Choose a task"` || body["thread_ts"] != nil {
			t.Fatal("invalid agent-view payload", body)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	}))
	if err := client.SetSuggestedPrompts(context.Background(), "D123", "Choose a task", []SuggestedPrompt{{Title: "Review", Message: "Review my PR"}}); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestSuggestedPromptsRejectMalformedRequestsAndKeepTypedErrors(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":"static_prompts_configured"}`))}, nil
	}))
	for _, entries := range [][]SuggestedPrompt{nil, {{Title: "empty message"}}, make([]SuggestedPrompt, 5)} {
		if err := client.SetSuggestedPrompts(context.Background(), "D1", "", entries); err == nil {
			t.Fatal("accepted invalid prompts")
		}
	}
	if calls != 0 {
		t.Fatal("invalid payload reached Slack")
	}
	err := client.SetSuggestedPrompts(context.Background(), "D1", "", []SuggestedPrompt{{Title: "Review", Message: "Review my PR"}})
	var api conversation.SlackError
	if !errors.As(err, &api) || api.SlackErrorCode() != "static_prompts_configured" {
		t.Fatal(err)
	}
}
