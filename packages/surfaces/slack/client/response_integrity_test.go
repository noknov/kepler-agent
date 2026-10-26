package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestSlackResponseMustBeOneCompleteBoundedJSONValue(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"multiple objects", `{"ok":true}{"ok":false}`},
		{"trailing text", `{"ok":true} trailing`},
		{"over byte budget", `{"ok":true}` + strings.Repeat(" ", 4<<20)},
	} {
		for _, replay := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/replay=%t", test.name, replay), func(t *testing.T) {
				calls := 0
				client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}))
				request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://slack.com/api/test", nil)
				var response struct {
					OK bool `json:"ok"`
				}
				err := client.doWithRetry(request, &response, replay)
				if err == nil {
					t.Error("accepted incomplete/oversized/multiple response values")
				}
				var uncertain conversation.DeliveryUncertain
				if !replay && (!errors.As(err, &uncertain) || !uncertain.DeliveryUncertain()) {
					t.Errorf("lost uncertain-write classification: %v", err)
				}
				if calls != 1 {
					t.Errorf("replayed invalid response: calls=%d", calls)
				}
			})
		}
	}
}
func TestSlackHTTPErrorsDoNotExposeUpstreamBodies(t *testing.T) {
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private upstream body with echoed credential"))}, nil
	}))
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://slack.com/api/test", nil)
	var out any
	err := client.do(request, &out)
	if err == nil || strings.Contains(err.Error(), "private upstream") || strings.Contains(err.Error(), "credential") {
		t.Fatalf("unsafe HTTP diagnostic: %v", err)
	}
}

func TestSlackCompleteResponseAtByteBoundaryRemainsValid(t *testing.T) {
	const prefix = `{"ok":true}`
	body := prefix + strings.Repeat(" ", maxSlackResponseBytes-len(prefix))
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://slack.com/api/test", nil)
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.do(request, &out); err != nil || !out.OK {
		t.Fatalf("valid boundary response rejected: %v", err)
	}
}

func TestStartStreamDoesNotPublishSuccessFromResponsePrefix(t *testing.T) {
	calls := 0
	client := NewTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"123.456"} corrupt-tail`))}, nil
	}))
	id, err := client.StartStream(context.Background(), conversation.StreamStart{Channel: "C1", ThreadTS: "1.0"})
	var uncertain conversation.DeliveryUncertain
	if id != "" || !errors.As(err, &uncertain) || !uncertain.DeliveryUncertain() || calls != 1 {
		t.Fatalf("published/replayed ambiguous stream: id=%q calls=%d err=%v", id, calls, err)
	}
}
