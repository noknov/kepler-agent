package llm

import "testing"

func TestOpenCodeGoUsesAnthropicForMessagesModels(t *testing.T) {
	tests := map[string]bool{
		"glm-5.2":           false,
		"kimi-k2.7-code":    false,
		"deepseek-v4-flash": false,
		"mimo-v2.5-pro":     false,
		"minimax-m3":        true,
		"minimax-m2.7":      true,
		"qwen3.7-max":       true,
		"qwen3.7-plus":      true,
		"qwen3.6-plus":      true,
	}
	for model, want := range tests {
		if got := openCodeGoUsesAnthropic(model); got != want {
			t.Fatalf("openCodeGoUsesAnthropic(%q) = %v, want %v", model, got, want)
		}
	}
}
