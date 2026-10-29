package providers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm"
)

func TestHTTPErrorClassificationUsesStatusAndErrorFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   model.ErrorKind
		retry  bool
	}{
		{"unrelated diagnostic", 400, `{"error":{"code":"invalid_schema","message":"Invalid schema"},"debug":"maximum context length"}`, model.ErrorInvalid, false},
		{"quoted policy phrase", 400, `{"error":{"message":"Invalid enum value: safety system","type":"invalid_request_error"}}`, model.ErrorInvalid, false},
		{"field length", 400, `{"error":{"code":"string_above_max_length","param":"tools[0].function.description"}}`, model.ErrorInvalid, false},
		{"authentication", 401, `{"error":{"code":"content_filter","message":"safety system"}}`, model.ErrorAuth, false},
		{"rate limit", 429, `{"error":{"code":"context_length_exceeded"}}`, model.ErrorRateLimited, true},
		{"server unavailable", 503, `{"error":{"code":"context_length_exceeded"}}`, model.ErrorUnavailable, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := toModelError(llm.NewProviderError("test", test.status, test.body))
			var typed *model.Error
			if model.ErrorKindOf(err) != test.want || !errors.As(err, &typed) || typed.Retryable != test.retry {
				t.Fatalf("classified status=%d as %s, want %s: %v", test.status, model.ErrorKindOf(err), test.want, err)
			}
		})
	}
}

func TestHTTPErrorClassificationPrecedesDiagnosticTruncation(t *testing.T) {
	body := `{"debug":"` + strings.Repeat("x", 2500) + `","error":{"code":"context_length_exceeded","message":"request rejected"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	for name, client := range map[string]llm.Client{
		"chat":      llm.NewOpenAICompatibleClient("test", server.URL, "", time.Second),
		"responses": llm.NewOpenAIResponsesClient("test", server.URL, "", time.Second),
		"anthropic": llm.NewAnthropicClient(server.URL, "", time.Second, "official"),
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", name, streaming), func(t *testing.T) {
				var err error
				if streaming {
					_, err = client.(llm.StreamClient).ChatStream(t.Context(), llm.Request{Model: "test"}, llm.StreamHandler{})
				} else {
					_, err = client.Chat(t.Context(), llm.Request{Model: "test"})
				}
				var providerErr llm.ProviderError
				if !errors.As(err, &providerErr) || len(providerErr.Body) > 2100 || model.ErrorKindOf(toModelError(err)) != model.ErrorContextLimit {
					t.Fatalf("stream=%v error=%v classification=%s", streaming, err, model.ErrorKindOf(toModelError(err)))
				}
			})
		}
	}
	t.Run("compaction", func(t *testing.T) {
		client := llm.NewOpenAIResponsesClient("test", server.URL, "", time.Second)
		_, err := client.Compact(t.Context(), llm.Request{Model: "test"})
		if model.ErrorKindOf(toModelError(err)) != model.ErrorContextLimit {
			t.Fatalf("compaction error lost classification: %v", err)
		}
	})
}
