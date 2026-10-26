package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestContextSearchUsesOneBoundedRTSPageWithEvidence(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-connected-user" {
			t.Error("search did not use its configured identity")
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/assistant.search.context" {
			t.Fatalf("unexpected wire target: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"query": `"What changed in Alpha?"`, "content_types": `["messages"]`, "channel_types": `["private_channel"]`, "context_channel_id": `"C1"`, "cursor": `"page2"`, "sort": `"score"`, "include_context_messages": `true`, "limit": `3`} {
			if string(body[key]) != want {
				t.Errorf("%s=%s want %s", key, body[key], want)
			}
		}
		if body["action_token"] != nil || body["token"] != nil {
			t.Error("credential escaped client boundary")
		}
		return jsonResp(`{"ok":true,"results":{"messages":[{"team_id":"T1","channel_id":"C1","message_ts":"2.0","thread_ts":"1.0","author_user_id":"U1","content":"Decision","permalink":"https://team.slack.com/archives/C1/p2000000","context_messages":{"before":[{"text":"Why?","user_id":"U2","ts":"1.0"}],"after":[{"text":"Agreed","ts":"3.0"}]}}]},"response_metadata":{"next_cursor":"page3"}}`), nil
	}))
	client.token = "test-connected-user"
	out, err := client.SearchContext(context.Background(), ContextSearchInput{Query: "What changed in Alpha?", ChannelTypes: []string{"private_channel"}, ContextChannelID: "C1", Cursor: "page2", Limit: 3})
	if err != nil || calls != 1 || len(out.Messages) != 1 || out.NextCursor != "page3" {
		t.Fatalf("out=%+v calls=%d err=%v", out, calls, err)
	}
	match := out.Messages[0]
	if match.ContextMessages.Before[0].UserID != "U2" || match.Permalink == "" || match.ThreadTS != "1.0" || len(match.ContextMessages.Before) != 1 || len(match.ContextMessages.After) != 1 {
		t.Fatalf("lost source/context: %+v", match)
	}
}

func TestContextSearchValidatesBeforeDispatch(t *testing.T) {
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid input dispatched"); return nil, nil }))
	for _, in := range []ContextSearchInput{{}, {Query: "x", Limit: -1}, {Query: "x", Limit: 21}, {Query: "x", ChannelTypes: []string{"all"}}} {
		if _, err := client.SearchContext(context.Background(), in); err == nil {
			t.Errorf("accepted invalid input: %+v", in)
		}
	}
}

func TestContextSearchRejectsInvalidEvidenceAndPreservesAPICode(t *testing.T) {
	for _, body := range []string{`{"ok":false,"error":"missing_scope"}`, `{"ok":true,"results":{"messages":[{"content":"unattributed"}]}}`, `{"ok":true,"results":{"messages":[{"channel_id":"C1","message_ts":"1.0","permalink":"url"},{"channel_id":"C1","message_ts":"2.0","permalink":"url"}]}}`} {
		calls := 0
		client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return jsonResp(body), nil }))
		out, err := client.SearchContext(context.Background(), ContextSearchInput{Query: "question", Limit: 1})
		if err == nil || len(out.Messages) != 0 || calls != 1 {
			t.Fatalf("published invalid search: out=%+v calls=%d err=%v", out, calls, err)
		}
		if body == `{"ok":false,"error":"missing_scope"}` {
			var code interface{ SlackErrorCode() string }
			if !errors.As(err, &code) || code.SlackErrorCode() != "missing_scope" {
				t.Fatalf("lost API code: %v", err)
			}
		}
	}
}
