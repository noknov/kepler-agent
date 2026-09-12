package cloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
)

func TestJoinUpstreamURL(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"https://api.openai.com/v1", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1", "/v1/responses", "https://api.openai.com/v1/responses"},
		{"https://api.anthropic.com", "/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"https://api.longcat.chat/anthropic", "/v1/messages", "https://api.longcat.chat/anthropic/v1/messages"},
	}
	for _, tc := range cases {
		got := JoinUpstreamURL(tc.base, tc.path)
		if got != tc.want {
			t.Fatalf("JoinUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestRestrictLLMProxyRejectsOtherModelAndCapsOutput(t *testing.T) {
	var received map[string]any
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { _ = json.NewDecoder(r.Body).Decode(&received) })
	handler := RestrictLLMProxy(next, config.LLMConfig{Model: "allowed", MaxOutputTokens: 100})
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"other"}`)))
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rejected.Code)
	}
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"allowed","max_output_tokens":999}`)))
	if received["model"] != "allowed" || received["max_output_tokens"] != float64(100) {
		t.Fatalf("received = %#v", received)
	}
}
