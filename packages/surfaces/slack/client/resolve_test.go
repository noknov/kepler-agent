package slack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseConversationLink(t *testing.T) {
	channel, thread, err := ParseConversationLink("https://example.slack.com/archives/C123/p1701234567890123?thread_ts=111.222&cid=C999")
	if err != nil || channel != "C999" || thread != "111.222" {
		t.Fatalf("channel=%q thread=%q err=%v", channel, thread, err)
	}
	channel, thread, err = ParseConversationLink("https://example.slack.com/archives/D0AJSE6PRLH/p1701234567890123")
	if err != nil || channel != "D0AJSE6PRLH" || thread != "1701234567.890123" {
		t.Fatalf("channel=%q thread=%q err=%v", channel, thread, err)
	}
}

func TestResolveReadTargetOpensIMFromUserID(t *testing.T) {
	client := testResolveClient(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/conversations.open"):
			return jsonResp(`{"ok":true,"channel":{"id":"DOPEN"}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	target, err := client.ResolveReadTarget(context.Background(), ReadTargetInput{
		User:         "U0AATUKVA7P",
		ScopeChannel: "D_BOT",
		ScopeThreadTS: "9.9",
		ScopeMessageTS: "9.9",
	})
	if err != nil || target.Channel != "DOPEN" || target.ThreadTS != "" || target.LatestTS != "" {
		t.Fatalf("target=%#v err=%v", target, err)
	}
}

func TestResolveReadTargetExplicitChannelDoesNotInheritScope(t *testing.T) {
	client := testResolveClient(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("explicit channel should not call API")
		return nil, nil
	})
	target, err := client.ResolveReadTarget(context.Background(), ReadTargetInput{
		Channel:        "D0AJSE6PRLH",
		ScopeChannel:   "D_BOT",
		ScopeThreadTS:  "9.9",
		ScopeMessageTS: "9.9",
	})
	if err != nil || target.Channel != "D0AJSE6PRLH" || target.ThreadTS != "" || target.LatestTS != "" {
		t.Fatalf("target=%#v err=%v", target, err)
	}
}

func TestResolveReadTargetFindsUserByName(t *testing.T) {
	client := testResolveClient(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/users.list"):
			return jsonResp(`{"ok":true,"members":[{"id":"U1","name":"stella","real_name":"Xiaoyu (Stella) Zhang","profile":{"display_name":"Stella"}}]}`), nil
		case strings.HasSuffix(r.URL.Path, "/api/conversations.open"):
			return jsonResp(`{"ok":true,"channel":{"id":"D_STELLA"}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	target, err := client.ResolveReadTarget(context.Background(), ReadTargetInput{User: "Stella Zhang"})
	if err != nil || target.Channel != "D_STELLA" {
		t.Fatalf("target=%#v err=%v", target, err)
	}
}

func testResolveClient(t *testing.T, fn roundTripFunc) *Client {
	t.Helper()
	return &Client{httpClient: &http.Client{Transport: fn}}
}

func jsonResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
