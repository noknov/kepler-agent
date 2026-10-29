package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type compactBodyClient struct{ *OpenAIResponsesClient }

func (c compactBodyClient) Chat(ctx context.Context, request Request) (Response, error) {
	return c.Compact(ctx, request)
}

func TestJSONResponseRequiresCompleteBoundedBody(t *testing.T) {
	for _, adapter := range []struct {
		name, payload, wantText string
		new                     func(string) Client
	}{
		{"chat", `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, "ok", func(url string) Client { return NewOpenAICompatibleClient("fixture", url, "", 0) }},
		{"responses", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`, "ok", func(url string) Client { return NewOpenAIResponsesClient("fixture", url, "", 0) }},
		{"anthropic", `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, "ok", func(url string) Client { return NewAnthropicClient(url, "", 0, "official") }},
		{"compact", `{"output":[{"type":"compaction","encrypted_content":"retained"}]}`, "", func(url string) Client { return compactBodyClient{NewOpenAIResponsesClient("fixture", url, "", 0)} }},
	} {
		for _, test := range []struct {
			name     string
			overflow bool
			status   int
		}{{"exact boundary", false, 200}, {"overflow after valid prefix", true, 200}, {"oversized rate limit", true, 429}} {
			t.Run(adapter.name+"/"+test.name, func(t *testing.T) {
				body := adapter.payload + strings.Repeat(" ", (8<<20)-len(adapter.payload))
				if test.overflow {
					body += "PRIVATE_TRAILING_RESPONSE"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(body))
				}))
				defer server.Close()
				response, err := adapter.new(server.URL).Chat(t.Context(), Request{Model: "fixture"})
				if test.status == 429 {
					if !IsRateLimited(err) {
						t.Fatalf("HTTP classification lost: %v", err)
					}
					return
				}
				if !test.overflow {
					if err != nil || response.Message.Content != adapter.wantText {
						t.Fatalf("complete boundary rejected: text=%q err=%v", response.Message.Content, err)
					}
					return
				}
				var protocol ProtocolError
				if !errors.As(err, &protocol) || response.Message.Content != "" || len(response.Message.ToolCalls) != 0 || strings.Contains(err.Error(), "PRIVATE_TRAILING_RESPONSE") {
					t.Fatalf("incomplete body accepted or exposed: text=%q raw_bytes=%d err=%v", response.Message.Content, len(response.Raw), err)
				}
			})
		}
	}
}
