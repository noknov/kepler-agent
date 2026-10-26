package slack

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestConversationPaginationRejectsCursorCycles(t *testing.T) {
	for _, method := range []string{"history", "replies"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				cursor := "repeat"
				if calls >= 3 {
					cursor = ""
				} // Finite fallback keeps the pre-fix regression from hanging.
				return jsonResp(fmt.Sprintf(`{"ok":true,"messages":[{"ts":"%d.0","text":"entry"}],"response_metadata":{"next_cursor":%q}}`, calls, cursor)), nil
			}))
			var err error
			if method == "history" {
				_, err = client.History(context.Background(), "C1", "", 10)
			} else {
				_, err = client.Replies(context.Background(), "C1", "1.0", 0)
			}
			if err == nil || !strings.Contains(err.Error(), "cursor") || calls != 2 {
				t.Fatalf("cycle accepted: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestConversationPaginationRequestsOnlyRemainingMessages(t *testing.T) {
	for _, method := range []string{"history", "replies"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResp(`{"ok":true,"messages":[{"text":"one"},{"text":"two"}],"response_metadata":{"next_cursor":"next"}}`), nil
				}
				if r.URL.Query().Get("limit") != "1" {
					t.Errorf("overfetched remaining budget: %s", r.URL.RawQuery)
				}
				return jsonResp(`{"ok":true,"messages":[{"text":"three"}]}`), nil
			}))
			var messages []Message
			var err error
			if method == "history" {
				messages, err = client.History(context.Background(), "C1", "", 3)
			} else {
				messages, err = client.Replies(context.Background(), "C1", "1.0", 3)
			}
			if err != nil || len(messages) != 3 || calls != 2 {
				t.Fatalf("messages=%d calls=%d err=%v", len(messages), calls, err)
			}
		})
	}
}

func TestConversationPaginationFollowsCursorThroughEmptyPage(t *testing.T) {
	for _, method := range []string{"history", "replies"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResp(`{"ok":true,"messages":[],"response_metadata":{"next_cursor":"next"}}`), nil
				}
				return jsonResp(`{"ok":true,"messages":[{"text":"found"}]}`), nil
			}))
			var messages []Message
			var err error
			if method == "history" {
				messages, err = client.History(context.Background(), "C1", "", 3)
			} else {
				messages, err = client.Replies(context.Background(), "C1", "1.0", 3)
			}
			if err != nil || len(messages) != 1 || calls != 2 {
				t.Fatalf("messages=%d calls=%d err=%v", len(messages), calls, err)
			}
		})
	}
}
