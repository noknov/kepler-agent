package cloud

import "testing"

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
