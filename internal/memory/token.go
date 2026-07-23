package memory

import (
	"encoding/json"
	"strings"

	"github.com/noknov/slack-copilot-agent/internal/llm"
)

const (
	// DefaultBytesPerToken is the default character-to-token ratio.
	// English text averages ~4 chars per token; CJK is closer to 1-2.
	DefaultBytesPerToken = 4

	// JSONBytesPerToken is the ratio for dense JSON content.
	// JSON has many single-character tokens ({, }, :, ,, "), making the
	// real ratio closer to 2.
	JSONBytesPerToken = 2
)

// RoughTokenEstimate returns a rough token count for the given text content
// using the default bytes-per-token ratio (4).
func RoughTokenEstimate(content string) int {
	return roughTokenEstimate(content, DefaultBytesPerToken)
}

// RoughTokenEstimateForToolResult returns a rough token count for tool result
// content. It detects JSON content and uses a denser ratio.
func RoughTokenEstimateForToolResult(content string) int {
	ratio := DefaultBytesPerToken
	if looksLikeJSON(content) {
		ratio = JSONBytesPerToken
	}
	return roughTokenEstimate(content, ratio)
}

// TokenCountFromUsage returns the total context window token count from an
// API response's usage data.
//
// For Anthropic-style APIs, cache tokens are independent of PromptTokens:
//
//	input_tokens + cache_creation_input_tokens + cache_read_input_tokens + output_tokens
//
// For OpenAI-compatible APIs (Usage.CacheIncludedInPrompt == true),
// CacheReadInputTokens is already a subset of PromptTokens, so adding it
// again would double-count. In that case we only sum the top-level fields:
//
//	prompt_tokens + completion_tokens
func TokenCountFromUsage(usage llm.Usage) int {
	if usage.CacheIncludedInPrompt {
		return usage.PromptTokens + usage.CompletionTokens
	}
	return usage.PromptTokens +
		usage.CacheCreationInputTokens +
		usage.CacheReadInputTokens +
		usage.CompletionTokens
}

// EstimateTokens returns a rough token estimate by summing all message content.
// Use CountTokensWithCalibration instead when API usage data is available.
func EstimateTokens(messages []llm.Message) int {
	total := 0
	for i := range messages {
		total += estimateMessageTokens(&messages[i])
	}
	return total
}

func EstimateToolSpecTokens(specs []llm.ToolSpec) int {
	total := 0
	for _, spec := range specs {
		data, err := json.Marshal(spec)
		if err != nil {
			total += RoughTokenEstimate(spec.Function.Name)
			total += RoughTokenEstimate(spec.Function.Description)
			continue
		}
		total += RoughTokenEstimateForToolResult(string(data))
	}
	return total
}

// CountTokensWithCalibration returns the best available token count for the
// message list. It mirrors claude-code's tokenCountWithEstimation:
//
//  1. Walk backwards to find the most recent assistant message with Usage.
//  2. Use that Usage as the precise baseline (the API already counted everything
//     up to and including that message).
//  3. Add rough estimates for any messages appended after that baseline.
//
// This is far more accurate than pure estimation because the API's token
// count includes the system prompt, tool definitions, and all overhead that
// we cannot see from the message list alone.
func CountTokensWithCalibration(messages []llm.Message) int {
	// Walk backwards to find the most recent message with real usage data.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Usage != nil && messages[i].Role == "assistant" {
			baseTokens := TokenCountFromUsage(*messages[i].Usage)
			// Estimate tokens for messages added after the calibrated one.
			added := 0
			for j := i + 1; j < len(messages); j++ {
				added += estimateMessageTokens(&messages[j])
			}
			return baseTokens + added
		}
	}
	// No calibrated data available — fall back to pure estimation.
	return EstimateTokens(messages)
}

// LastUsage returns the Usage from the most recent assistant message, or nil.
func LastUsage(messages []llm.Message) *llm.Usage {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].Usage != nil {
			return messages[i].Usage
		}
	}
	return nil
}

// --- internal helpers ---

func roughTokenEstimate(content string, bytesPerToken int) int {
	if bytesPerToken <= 0 {
		bytesPerToken = DefaultBytesPerToken
	}
	n := len([]rune(content))
	if n == 0 {
		return 0
	}
	tokens := n / bytesPerToken
	if tokens == 0 {
		tokens = 1
	}
	return tokens
}

func estimateMessageTokens(msg *llm.Message) int {
	tokens := 0

	// Text content
	if msg.Content != "" {
		tokens += RoughTokenEstimate(msg.Content)
	}

	// Content parts (images, text parts)
	for _, part := range msg.ContentParts {
		switch part.Type {
		case "text":
			tokens += RoughTokenEstimate(part.Text)
		case "image_url":
			// Images are billed at a fixed rate; use a conservative estimate.
			tokens += 2000
		}
	}

	// Reasoning content (thinking blocks)
	if msg.ReasoningContent != "" {
		tokens += RoughTokenEstimate(msg.ReasoningContent)
	}

	// Tool calls (the model's tool invocation JSON)
	for _, tc := range msg.ToolCalls {
		tokens += RoughTokenEstimate(tc.Function.Name)
		if tc.Function.Arguments != "" {
			tokens += RoughTokenEstimateForToolResult(tc.Function.Arguments)
		}
	}

	// Tool call ID overhead
	if msg.ToolCallID != "" {
		tokens += 2 // small fixed overhead for the ID field
	}

	return tokens
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	return (s[0] == '{' && s[len(s)-1] == '}') || (s[0] == '[' && s[len(s)-1] == ']')
}
