package llm

import (
	"net/http/httptest"
	"testing"
)

func TestSetOpenCodeSessionHeader(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider string
		metadata map[string]string
		want     string
	}{
		{name: "OpenCode Go", provider: "opencode-go", metadata: map[string]string{"session_id": "thread-1"}, want: "thread-1"},
		{name: "trims session ID", provider: "opencode-go", metadata: map[string]string{"session_id": " thread-1 "}, want: "thread-1"},
		{name: "other provider", provider: "openai", metadata: map[string]string{"session_id": "thread-1"}},
		{name: "missing session ID", provider: "opencode-go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "https://example.test", nil)
			setOpenCodeSessionHeader(req, test.provider, test.metadata)
			if got := req.Header.Get("x-opencode-session"); got != test.want {
				t.Fatalf("x-opencode-session = %q, want %q", got, test.want)
			}
		})
	}
}
