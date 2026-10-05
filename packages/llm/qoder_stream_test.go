package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestQoderStreamUsesActivityTimeoutAndCallerDeadline(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active", false: "silent"}[active], func(t *testing.T) {
			client := &http.Client{Timeout: 100 * time.Millisecond, Transport: qoderTestTransport(func(req *http.Request) (*http.Response, error) {
				reader, writer := io.Pipe()
				go func() {
					defer writer.Close()
					for i := 0; i < 6; i++ {
						select {
						case <-req.Context().Done():
							writer.CloseWithError(req.Context().Err())
							return
						case <-time.After(30 * time.Millisecond):
						}
						if active {
							if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
								return
							}
						}
					}
					io.WriteString(writer, qoderFixture(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"))
				}()
				return &http.Response{StatusCode: 200, Body: reader, Header: make(http.Header)}, nil
			})}
			request, _ := http.NewRequestWithContext(context.Background(), "POST", "https://synthetic.test", nil)
			response, err := qoderStreamRequest(client, request, StreamHandler{})
			if active && (err != nil || response.Message.Content != "ok") {
				t.Fatalf("healthy stream interrupted: %v", err)
			}
			if !active && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("silent stream accepted: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			request = request.WithContext(ctx)
			_, err = qoderStreamRequest(client, request, StreamHandler{})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation lost: %v", err)
			}
		})
	}
}
