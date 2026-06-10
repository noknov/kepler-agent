package embedding

import "testing"

func TestTruncateBody(t *testing.T) {
	tests := []struct {
		body string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"longer text here", 6, "longer..."},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncateBody([]byte(tt.body), tt.max)
		if got != tt.want {
			t.Errorf("truncateBody(%q, %d) = %q, want %q", tt.body, tt.max, got, tt.want)
		}
	}
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("https://api.openai.com/v1", "sk-test", "", 0)
	if c.Model != "text-embedding-3-small" {
		t.Errorf("default model = %q, want text-embedding-3-small", c.Model)
	}
	if c.Dims != 1536 {
		t.Errorf("default dims = %d, want 1536", c.Dims)
	}
	if c.HTTPClient == nil {
		t.Error("HTTP client should not be nil")
	}
}
