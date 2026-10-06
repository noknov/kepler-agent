package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func TestTransportEOFUsesTypedClassification(t *testing.T) {
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF} {
		err := toModelError(fmt.Errorf("response body: %w", cause))
		var typed *model.Error
		if !errors.As(err, &typed) || typed.Kind != model.ErrorTransient || !typed.Retryable || !errors.Is(err, cause) {
			t.Fatalf("transport error not preserved/classified: %v", err)
		}
	}
	plain := errors.New("unexpected EOF")
	if toModelError(plain) != plain {
		t.Fatal("classification must use error identity, not message matching")
	}
	auth := &model.Error{Kind: model.ErrorAuth, Cause: io.EOF}
	if toModelError(auth) != auth {
		t.Fatal("explicit provider error classification must be preserved")
	}
}

func TestHostedTransportEOFRetriesOnlyBeforeVisibleOutput(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				if calls.Add(1) == 1 {
					// A truncated HTTP body, rather than a simulated error string.
					w.Header().Set("Content-Length", "100000")
					event := model.StreamEvent{Type: model.StreamStarted}
					if committed {
						event = model.StreamEvent{Type: model.StreamTextDelta, Text: "visible"}
					}
					_ = json.NewEncoder(w).Encode(HostedStreamLine{Kind: "event", Event: &event})
					return
				}
				_ = json.NewEncoder(w).Encode(HostedStreamLine{Kind: "result", Response: &model.Response{Message: model.TextMessage(model.RoleAssistant, "ok"), FinishReason: model.FinishStop}})
			}))
			defer server.Close()
			client, err := New(Config{Provider: "kepler", Protocol: "kepler", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			resilient := &model.ResilientClient{Primary: client, MaxAttempts: 2, RetryDelay: time.Nanosecond}
			response, err := resilient.Generate(context.Background(), model.Request{Model: "test"}, func(model.StreamEvent) error { return nil })
			if committed {
				if calls.Load() != 1 || model.ErrorKindOf(err) != model.ErrorOutputCommitted {
					t.Fatalf("unsafe replay: calls=%d err=%v", calls.Load(), err)
				}
			} else if err != nil || calls.Load() != 2 || response.Message.Text() != "ok" {
				t.Fatalf("safe recovery failed: calls=%d response=%+v err=%v", calls.Load(), response, err)
			}
		})
	}
}
