package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerSchemeCaseDoesNotBecomePartOfCredential(t *testing.T) {
	const token = "bEaReR fixture-token"
	for _, adapter := range []struct {
		name, payload, auth, keyHeader string
		new                            func(string, string) Client
	}{
		{"chat", `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, token, "", func(url, key string) Client { return NewOpenAICompatibleClient("fixture", url, key, 0) }},
		{"api-key", `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, "", "api-key", func(url, key string) Client { return NewOpenAICompatibleClient("mimo", url, key, 0) }},
		{"responses", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`, token, "", func(url, key string) Client { return NewOpenAIResponsesClient("fixture", url, key, 0) }},
		{"anthropic", `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, token, "x-api-key", func(url, key string) Client { return NewAnthropicClient(url, key, 0, "official") }},
		{"claude-code", `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, token, "", func(url, key string) Client { return NewAnthropicClient(url, key, 0, "claude-code") }},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != adapter.auth || (adapter.keyHeader != "" && r.Header.Get(adapter.keyHeader) != "fixture-token") {
					http.Error(w, "invalid credentials", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(adapter.payload))
			}))
			defer server.Close()
			if response, err := adapter.new(server.URL, token).Chat(t.Context(), Request{Model: "fixture"}); err != nil || response.Message.Content != "ok" {
				t.Fatalf("valid bearer scheme rejected: %v", err)
			}
		})
	}
}
