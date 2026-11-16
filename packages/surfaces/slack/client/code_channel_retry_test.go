package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func nativeWriteOperations() map[string]func(*Client) error {
	return map[string]func(*Client) error{
		"context": func(c *Client) error { return c.SetCodeContext(context.Background(), "C1", nil) },
		"view": func(c *Client) error {
			return c.SetCodeView(context.Background(), CodeView{Channel: "C1", Type: "html", Key: "report", Content: "old revision"})
		},
		"canvas": func(c *Client) error { return c.SetCodeCanvasContent(context.Background(), "C1", "F1", "old revision") },
		"remove": func(c *Client) error { return c.RemoveCodeView(context.Background(), "C1", "V1") },
	}
}

func TestNativeWritesDoNotReplayUncertainResults(t *testing.T) {
	for name, operation := range nativeWriteOperations() {
		for _, failure := range []string{"transport", "server", "internal_error", "fatal_error", "missing_result", "broken_json"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					calls := 0
					client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						if failure == "transport" {
							return nil, io.ErrUnexpectedEOF
						}
						status, body := 200, `{"ok":true}`
						switch failure {
						case "server":
							status = 503
						case "internal_error", "fatal_error":
							body = `{"ok":false,"error":"` + failure + `"}`
						case "missing_result":
							body = `{}`
						case "broken_json":
							body = `{"ok":true`
						}
						return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
					}))
					err := operation(client)
					var uncertain conversation.DeliveryUncertain
					if calls != 1 || !errors.As(err, &uncertain) || !uncertain.DeliveryUncertain() {
						t.Fatalf("calls=%d err=%v; old native writes must not be replayed", calls, err)
					}
				})
			})
		}
	}
}

func TestNativeWritesRetryExplicitRateLimitRejection(t *testing.T) {
	for name, operation := range nativeWriteOperations() {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var bodies []string
				client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body, _ := io.ReadAll(req.Body)
					bodies = append(bodies, string(body))
					status, response := 200, `{"ok":true}`
					if len(bodies) == 1 {
						status = 429
						response = `{"ok":false,"error":"ratelimited"}`
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"1"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				}))
				if err := operation(client); err != nil || len(bodies) != 2 || bodies[0] != bodies[1] {
					t.Fatalf("bodies=%v err=%v", bodies, err)
				}
			})
		})
	}
}

func TestNativeReadRetriesRemainEnabled(t *testing.T) {
	for _, method := range []string{"views", "canvas"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					status, body := 200, `{"ok":true,"views":[],"canvas_id":"F1","content":"Plan"}`
					if calls == 1 {
						status = 503
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				}))
				var err error
				if method == "views" {
					_, err = client.ListCodeViews(context.Background(), "C1")
				} else {
					_, err = client.GetCodeCanvas(context.Background(), "C1", "F1", false)
				}
				if err != nil || calls != 2 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			})
		})
	}
}
