package llm

import "testing"

func TestOpenCodeGoUsesResponsesForLuna(t *testing.T) {
	tests := map[string]bool{
		"gpt-5.6-luna": true,
		"GPT-5.6-LUNA": true,
		"glm-5.2":      false,
		"minimax-m3":   false,
	}
	for model, want := range tests {
		if got := openCodeGoUsesResponses(model); got != want {
			t.Fatalf("openCodeGoUsesResponses(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestOpenCodeGoUsesAnthropicForMessagesModels(t *testing.T) {
	tests := map[string]bool{
		"glm-5.2":           false,
		"gpt-5.6-luna":      false,
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
