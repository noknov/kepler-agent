package memory

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/llm"
)

func TestRoughTokenEstimate(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 0},
		{"short", "hello", 1},
		{"typical sentence", "The quick brown fox jumps over the lazy dog", 10},
		{"long text", string(make([]byte, 400)), 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoughTokenEstimate(tt.content)
			if got != tt.want {
				t.Errorf("RoughTokenEstimate(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestRoughTokenEstimateForToolResult(t *testing.T) {
	jsonContent := `{"key": "value", "nested": {"a": 1, "b": 2}}`
	textContent := "This is a plain text result from a tool call"

	jsonTokens := RoughTokenEstimateForToolResult(jsonContent)
	textTokens := RoughTokenEstimateForToolResult(textContent)

	// JSON should use denser ratio (2 vs 4), so JSON tokens should be higher
	// for similar-length content.
	if jsonTokens <= 0 {
		t.Errorf("JSON token estimate should be > 0, got %d", jsonTokens)
	}
	if textTokens <= 0 {
		t.Errorf("Text token estimate should be > 0, got %d", textTokens)
	}

	// For the same length, JSON should give ~2x more tokens.
	sameLength := "abcdefghij" // 10 chars
	jsonEst := RoughTokenEstimateForToolResult("[" + sameLength + "]")
	textEst := RoughTokenEstimateForToolResult(sameLength)
	// JSON estimate should be roughly double (bytes/2 vs bytes/4).
	if jsonEst <= textEst {
		t.Errorf("JSON estimate (%d) should be > text estimate (%d) for similar length", jsonEst, textEst)
	}
}

func TestTokenCountFromUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage llm.Usage
		want  int
	}{
		{
			name: "anthropic style - cache tokens are independent",
			usage: llm.Usage{
				PromptTokens:             1000,
				CompletionTokens:         500,
				CacheReadInputTokens:     200,
				CacheCreationInputTokens: 100,
				CacheIncludedInPrompt:    false,
			},
			want: 1800, // 1000 + 500 + 200 + 100
		},
		{
			name: "openai style - cache tokens already in prompt_tokens",
			usage: llm.Usage{
				PromptTokens:          18000,
				CompletionTokens:      500,
				CacheReadInputTokens:  5000, // subset of PromptTokens, must NOT be added again
				CacheIncludedInPrompt: true,
			},
			want: 18500, // 18000 + 500 (no double-count of 5000)
		},
		{
			name: "openai style - no cache hit",
			usage: llm.Usage{
				PromptTokens:          10000,
				CompletionTokens:      300,
				CacheIncludedInPrompt: true,
			},
			want: 10300,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TokenCountFromUsage(tt.usage)
			if got != tt.want {
				t.Errorf("TokenCountFromUsage() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello, how are you?"},
		{Role: "assistant", Content: "I'm doing well, thank you for asking!"},
	}
	tokens := EstimateTokens(messages)
	if tokens <= 0 {
		t.Error("EstimateTokens should return > 0 for non-empty messages")
	}
	// Rough check: ~90 chars total / 4 = ~22 tokens, allow some margin.
	if tokens < 10 || tokens > 50 {
		t.Errorf("EstimateTokens = %d, expected roughly 10-50 for these messages", tokens)
	}
}

func TestEstimateToolSpecTokens(t *testing.T) {
	specs := []llm.ToolSpec{{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "large-tool",
			Description: "tool with a non-trivial schema",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "search query"},
				},
			},
		},
	}}
	if got := EstimateToolSpecTokens(specs); got <= 0 {
		t.Fatalf("EstimateToolSpecTokens() = %d, want > 0", got)
	}
}

func TestCountTokensWithCalibration(t *testing.T) {
	// Without any usage data, should fall back to estimation.
	messages := []llm.Message{
		{Role: "system", Content: "System prompt here"},
		{Role: "user", Content: "User message"},
		{Role: "assistant", Content: "Assistant reply"},
	}
	estimated := CountTokensWithCalibration(messages)
	if estimated <= 0 {
		t.Error("should return > 0 even without calibration")
	}

	// With calibration data on the assistant message.
	usage := llm.Usage{
		PromptTokens:     5000,
		CompletionTokens: 100,
	}
	messages[2].Usage = &usage
	calibrated := CountTokensWithCalibration(messages)
	// Should be close to 5100 (from usage) + 0 (nothing after the calibrated msg).
	if calibrated < 5000 {
		t.Errorf("calibrated count %d should be >= 5000 (from usage)", calibrated)
	}

	// Add a new message after the calibrated one.
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: "A new user message that was not counted by the API",
	})
	withNew := CountTokensWithCalibration(messages)
	// Should be usage base + estimate for new message.
	if withNew <= calibrated {
		t.Errorf("count with new message (%d) should be > calibrated (%d)", withNew, calibrated)
	}
}

func TestLastUsage(t *testing.T) {
	usage1 := llm.Usage{PromptTokens: 100}
	usage2 := llm.Usage{PromptTokens: 200}

	messages := []llm.Message{
		{Role: "system", Content: "prompt"},
		{Role: "assistant", Content: "reply 1", Usage: &usage1},
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "reply 2", Usage: &usage2},
	}
	got := LastUsage(messages)
	if got == nil {
		t.Fatal("LastUsage should not be nil")
	}
	if got.PromptTokens != 200 {
		t.Errorf("LastUsage should return most recent, got PromptTokens=%d, want 200", got.PromptTokens)
	}

	// No usage data.
	noUsage := []llm.Message{
		{Role: "user", Content: "hello"},
	}
	if LastUsage(noUsage) != nil {
		t.Error("LastUsage should return nil when no usage data")
	}
}
