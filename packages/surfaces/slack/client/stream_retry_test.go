package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestStreamWritesRetryOnlyRateLimitRejections(t *testing.T) {
	for _, operation := range []string{"start", "append"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			var bodies []string
			client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				data, _ := io.ReadAll(req.Body)
				bodies = append(bodies, string(data))
				status, body := http.StatusOK, `{"ok":true,"ts":"1.0"}`
				if calls == 1 {
					status, body = http.StatusTooManyRequests, `{"ok":false,"error":"ratelimited"}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			}))
			var err error
			if operation == "start" {
				_, err = client.StartStream(context.Background(), slackconversation.StreamStart{Channel: "C", ThreadTS: "T"})
			} else {
				err = client.AppendStream(context.Background(), "C", "1.0", []map[string]any{{"type": "markdown_text", "text": "回答"}})
			}
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if bodies[0] != bodies[1] {
				t.Fatal("request body changed on retry")
			}
		})
	}
}

func TestStreamStartMarksUncertainResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server_error", 503, `{"ok":false}`},
		{"internal_error", 200, `{"ok":false,"error":"internal_error"}`},
		{"fatal_error", 200, `{"ok":false,"error":"fatal_error"}`},
		{"missing_result", 200, `{}`},
		{"broken_json", 200, `{"ok":true`},
		{"missing_message_id", 200, `{"ok":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			}))
			_, err := client.StartStream(context.Background(), slackconversation.StreamStart{Channel: "C", ThreadTS: "T"})
			var uncertain slackconversation.DeliveryUncertain
			if !errors.As(err, &uncertain) || !uncertain.DeliveryUncertain() || calls != 1 {
				t.Fatalf("calls=%d err=%v; must not send a duplicate reply", calls, err)
			}
		})
	}
}

func TestStreamRateLimitWaitRespectsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
		}))
		_, err := client.StartStream(ctx, slackconversation.StreamStart{Channel: "C", ThreadTS: "T"})
		// Retry-After exceeds the request deadline. No immediate/fallback retry or
		// sleep beyond cancellation is allowed. synctest advances virtual time.
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		var uncertain slackconversation.DeliveryUncertain
		if errors.As(err, &uncertain) {
			t.Fatalf("explicit rate-limit rejection is not uncertain: %v", err)
		}
	})
}

func TestStreamRateLimitRetriesAreBounded(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	}))
	err := client.AppendStream(context.Background(), "C", "1.0", nil)
	if err == nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestPostMessageRetriesRequireStableIdentity(t *testing.T) {
	for _, id := range []string{"", "stable-id"} {
		t.Run("id="+id, func(t *testing.T) {
			calls := 0
			client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				status, body := 200, `{"ok":true,"ts":"1.0"}`
				if calls == 1 {
					status, body = 503, `{"ok":false}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			}))
			_, err := client.postMessage(context.Background(), "C", "T", "hello", nil, id, false)
			if id == "" {
				var uncertain slackconversation.DeliveryUncertain
				if calls != 1 || !errors.As(err, &uncertain) {
					t.Fatalf("non-idempotent post: calls=%d err=%v", calls, err)
				}
			} else if calls != 2 || err != nil {
				t.Fatalf("idempotent post: calls=%d err=%v", calls, err)
			}
		})
	}
}
