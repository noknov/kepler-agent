package memory

import (
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/llm"
)

func TestCompactorMicroCompactClearsOldTools(t *testing.T) {
	c := &Compactor{
		KeepRecentTools: 3,
		ClearableTools:  map[string]bool{"code-read_file": true, "code-search": true},
	}

	messages := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "assistant"},
		{Role: "tool", Name: "code-read_file", ToolCallID: "1", Content: strings.Repeat("file content ", 50)},
		{Role: "assistant"},
		{Role: "tool", Name: "code-search", ToolCallID: "2", Content: strings.Repeat("search results ", 50)},
		{Role: "assistant"},
		{Role: "tool", Name: "code-read_file", ToolCallID: "3", Content: "recent 1"},
		{Role: "assistant"},
		{Role: "tool", Name: "code-search", ToolCallID: "4", Content: "recent 2"},
		{Role: "assistant"},
		{Role: "tool", Name: "code-read_file", ToolCallID: "5", Content: "recent 3"},
	}

	result := c.ApplyMicroCompact(messages)

	// Old tool results should be cleared.
	if result[2].Content != ToolResultClearedMsg {
		t.Errorf("expected tool [1] to be cleared, got: %s", truncateForTest(result[2].Content, 60))
	}
	if result[4].Content != ToolResultClearedMsg {
		t.Errorf("expected tool [2] to be cleared, got: %s", truncateForTest(result[4].Content, 60))
	}

	// Recent tool results should be preserved.
	if result[6].Content != "recent 1" {
		t.Errorf("expected tool [3] to be preserved, got: %s", result[6].Content)
	}
	if result[8].Content != "recent 2" {
		t.Errorf("expected tool [4] to be preserved, got: %s", result[8].Content)
	}
	if result[10].Content != "recent 3" {
		t.Errorf("expected tool [5] to be preserved, got: %s", result[10].Content)
	}

	// Original messages should not be mutated.
	if messages[2].Content == ToolResultClearedMsg {
		t.Error("original messages should not be mutated")
	}
}

func TestCompactorMicroCompactPreservesNonClearableTools(t *testing.T) {
	c := &Compactor{
		KeepRecentTools: 1,
		ClearableTools:  map[string]bool{"code-read_file": true},
	}

	messages := []llm.Message{
		{Role: "assistant"},
		{Role: "tool", Name: "slack-ask_user", ToolCallID: "1", Content: "user response"},
		{Role: "assistant"},
		{Role: "tool", Name: "code-read_file", ToolCallID: "2", Content: "latest"},
	}

	result := c.ApplyMicroCompact(messages)

	// Non-clearable tool should be preserved even though it's old.
	if result[1].Content != "user response" {
		t.Errorf("non-clearable tool should be preserved, got: %s", result[1].Content)
	}
}

func TestCompactorMicroCompactNoOpWhenFewTools(t *testing.T) {
	c := &Compactor{
		KeepRecentTools: 8,
		ClearableTools:  map[string]bool{"code-read_file": true},
	}

	messages := []llm.Message{
		{Role: "system", Content: "prompt"},
		{Role: "assistant"},
		{Role: "tool", Name: "code-read_file", ToolCallID: "1", Content: "short"},
	}

	result := c.ApplyMicroCompact(messages)

	// Should be unchanged — fewer tool results than keep threshold.
	if result[2].Content != "short" {
		t.Error("should not compress when under tool count limit")
	}
}

func TestCompactorCompressToolResults(t *testing.T) {
	c := &Compactor{
		MaxToolResultTokens: 10, // very small for testing
	}

	largeContent := strings.Repeat("x", 10000) // ~2500 tokens at 4 chars/token
	messages := []llm.Message{
		{Role: "assistant"},
		{Role: "tool", Name: "code-read_file", ToolCallID: "1", Content: largeContent},
		{Role: "assistant", Content: "small text"},
	}

	result := c.compressToolResults(messages)

	// Large tool result should be compressed.
	if len([]rune(result[1].Content)) >= len([]rune(largeContent)) {
		t.Error("large tool result should be compressed")
	}

	// Small assistant message should be unchanged.
	if result[2].Content != "small text" {
		t.Error("non-tool messages should be unchanged")
	}

	// Compressed content should contain the truncation marker.
	if !strings.Contains(result[1].Content, "truncated") {
		t.Error("compressed content should contain truncation marker")
	}
}

func TestCompactorThreshold(t *testing.T) {
	c := &Compactor{
		MaxContextTokens:  200000,
		AutocompactBuffer: 13000,
		OutputReserve:     20000,
	}
	expected := 200000 - 13000 - 20000 // 167000
	if c.Threshold() != expected {
		t.Errorf("Threshold() = %d, want %d", c.Threshold(), expected)
	}
}

func TestCompactorThresholdDefaults(t *testing.T) {
	c := &Compactor{}
	expected := DefaultMaxContextTokens - DefaultAutocompactBuffer - DefaultOutputReserve
	if c.Threshold() != expected {
		t.Errorf("Threshold() with defaults = %d, want %d", c.Threshold(), expected)
	}
}

func TestTruncateHeadTail(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		wantLen  int // approximate expected max length
	}{
		{
			"under limit",
			"short text",
			100,
			10, // unchanged
		},
		{
			"over limit",
			strings.Repeat("a", 1000),
			200,
			200, // should be truncated
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateHeadTail(tt.input, tt.maxRunes)
			if tt.name == "under limit" && got != tt.input {
				t.Errorf("under limit should be unchanged")
			}
			if tt.name == "over limit" {
				if len([]rune(got)) > tt.maxRunes+50 { // allow for marker
					t.Errorf("over limit: got %d runes, max should be ~%d", len([]rune(got)), tt.maxRunes)
				}
				if !strings.Contains(got, "truncated") {
					t.Error("should contain truncation marker")
				}
			}
		})
	}
}

func TestExtractSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"with analysis and summary tags",
			"<analysis>thinking process here</analysis>\n<summary>the actual summary</summary>",
			"the actual summary",
		},
		{
			"summary only",
			"<summary>just the summary</summary>",
			"just the summary",
		},
		{
			"no tags",
			"plain text summary without tags",
			"plain text summary without tags",
		},
		{
			"analysis without closing",
			"<analysis>incomplete analysis <summary>the summary</summary>",
			"the summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSummary(tt.input)
			if got != tt.want {
				t.Errorf("extractSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCompactUserMessage(t *testing.T) {
	summary := "1. Primary Request: Fix the bug\n2. Key Concepts: Go, context management"
	result := FormatCompactUserMessage(summary)

	if !strings.Contains(result, "continued from a previous conversation") {
		t.Error("should contain continuation prefix")
	}
	if !strings.Contains(result, "Primary Request") {
		t.Error("should contain the summary")
	}
	if !strings.Contains(result, "Continue the conversation") {
		t.Error("should contain continuation instruction")
	}
}

func TestCircuitBreaker(t *testing.T) {
	c := &Compactor{}

	// Initially not tripped.
	if c.consecutiveFailures >= MaxConsecutiveCompactFailures {
		t.Error("circuit breaker should not be tripped initially")
	}

	// Simulate failures.
	for i := 0; i < MaxConsecutiveCompactFailures; i++ {
		c.mu.Lock()
		c.consecutiveFailures++
		c.mu.Unlock()
	}

	c.mu.Lock()
	failures := c.consecutiveFailures
	c.mu.Unlock()
	if failures < MaxConsecutiveCompactFailures {
		t.Error("circuit breaker should be tripped after N failures")
	}

	// Reset on success.
	c.RecordCompactSuccess()
	c.mu.Lock()
	failures = c.consecutiveFailures
	c.mu.Unlock()
	if failures != 0 {
		t.Errorf("circuit breaker should reset on success, got %d", failures)
	}
}

func TestMessageImportanceScore(t *testing.T) {
	tests := []struct {
		name    string
		msg     llm.Message
		minScore int
	}{
		{
			"user message",
			llm.Message{Role: "user", Content: "fix the bug"},
			10, // user messages get base 10
		},
		{
			"error in tool result",
			llm.Message{Role: "tool", Content: "error: connection timeout"},
			5, // error keyword
		},
		{
			"assistant with tool calls",
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Function: llm.ToolFunction{Name: "code-search"}}}},
			3, // tool calls get 3
		},
		{
			"plain assistant",
			llm.Message{Role: "assistant", Content: "I'll help you with that"},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := messageImportanceScore(&tt.msg)
			if score < tt.minScore {
				t.Errorf("messageImportanceScore() = %d, want >= %d", score, tt.minScore)
			}
		})
	}
}

func TestRepairToolPairing(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "prompt"},
		// Orphaned tool result (no matching assistant tool_use)
		{Role: "tool", Name: "code-search", ToolCallID: "orphan", Content: "result"},
		// Valid pair
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "valid", Function: llm.ToolFunction{Name: "code-read_file"}},
		}},
		{Role: "tool", Name: "code-read_file", ToolCallID: "valid", Content: "file content"},
	}

	result := repairToolPairing(messages)

	// System should be kept.
	if result[0].Role != "system" {
		t.Error("system message should be kept")
	}

	// Orphaned tool result should be removed.
	for _, msg := range result {
		if msg.ToolCallID == "orphan" {
			t.Error("orphaned tool result should be removed")
		}
	}

	// Valid pair should be kept.
	foundValid := false
	for _, msg := range result {
		if msg.ToolCallID == "valid" {
			foundValid = true
			break
		}
	}
	if !foundValid {
		t.Error("valid tool result should be kept")
	}
}

// --- helpers ---

func truncateForTest(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
