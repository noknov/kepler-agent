package memory

import (
	"context"
	"strings"
	"sync"

	"github.com/wati/oncall-agent/internal/llm"
)

const (
	// DefaultMaxContextTokens is the default context window size (200K tokens).
	DefaultMaxContextTokens = 200_000

	// DefaultAutocompactBuffer is reserved headroom before auto-compact triggers.
	// Matches claude-code's AUTOCOMPACT_BUFFER_TOKENS.
	DefaultAutocompactBuffer = 13_000

	// DefaultOutputReserve is the token budget reserved for model output.
	// Matches claude-code: min(maxOutputTokens, 20_000).
	DefaultOutputReserve = 20_000

	// DefaultKeepRecentTools is the number of recent tool results to preserve.
	// Matches claude-code microcompact behavior.
	DefaultKeepRecentTools = 8

	// DefaultMaxToolResultTokens caps each individual tool result.
	DefaultMaxToolResultTokens = 5_000

	// MaxConsecutiveCompactFailures stops retrying after N consecutive failures.
	// claude-code learned this the hard way: 1,279 sessions had 50+ failures,
	// wasting ~250K API calls/day.
	MaxConsecutiveCompactFailures = 3

	// ToolResultClearedMsg replaces old tool results in Layer 1.
	ToolResultClearedMsg = "[Old tool result content cleared]"
)

// Compactor orchestrates 4 layers of context compression, from cheapest
// (zero API cost) to most expensive (LLM call). Each layer is tried in order;
// the next layer catches overflow that the previous couldn't resolve.
type Compactor struct {
	// MaxContextTokens is the context window limit in tokens.
	MaxContextTokens int

	// AutocompactBuffer is the token headroom before Layer 4 triggers.
	AutocompactBuffer int

	// OutputReserve is tokens reserved for model output.
	OutputReserve int

	// KeepRecentTools is the count of recent tool results preserved in Layer 1.
	KeepRecentTools int

	// MaxToolResultTokens caps each tool result in Layer 2.
	MaxToolResultTokens int

	// ClearableTools lists tool names whose results can be cleared in Layer 1.
	ClearableTools map[string]bool

	// LLMClient is used by Layer 4 for LLM-driven summaries.
	LLMClient llm.Client

	// CompactModel is the model used for Layer 4 summaries (can be cheaper).
	CompactModel string

	// circuit breaker state
	mu                  sync.Mutex
	consecutiveFailures int
}

// CompactResult contains metadata about a compaction operation.
type CompactResult struct {
	Layer             string // which layer performed the compression
	PreTokens         int    // estimated tokens before compression
	PostTokens        int    // estimated tokens after compression
	Summary           string // Layer 4 summary (empty for layers 1-3)
	CircuitBreakerHit bool   // true if Layer 4 is disabled due to failures
}

// Threshold returns the token count at which Layer 4 (LLM compact) triggers.
func (c *Compactor) Threshold() int {
	maxTokens := c.MaxContextTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxContextTokens
	}
	buffer := c.AutocompactBuffer
	if buffer <= 0 {
		buffer = DefaultAutocompactBuffer
	}
	reserve := c.OutputReserve
	if reserve <= 0 {
		reserve = DefaultOutputReserve
	}
	return maxTokens - buffer - reserve
}

// ApplyMicroCompact runs Layer 1 only — clearing old tool results.
// This is safe to call on every step of the agent loop (zero API cost).
func (c *Compactor) ApplyMicroCompact(messages []llm.Message) []llm.Message {
	return c.microCompact(messages)
}

// CompactIfNeeded runs all compression layers as needed.
// It is called between conversation turns (not every step) to manage
// persistent session context.
func (c *Compactor) CompactIfNeeded(ctx context.Context, messages []llm.Message) ([]llm.Message, *CompactResult, error) {
	return c.CompactIfNeededWithReserve(ctx, messages, 0)
}

// CompactIfNeededWithReserve runs compaction while reserving additional tokens
// for transient request payload such as tool schemas. The returned PreTokens and
// PostTokens describe message tokens only; reserveTokens only lowers the
// effective threshold.
func (c *Compactor) CompactIfNeededWithReserve(ctx context.Context, messages []llm.Message, reserveTokens int) ([]llm.Message, *CompactResult, error) {
	if len(messages) == 0 {
		return messages, nil, nil
	}

	result := &CompactResult{}
	threshold := c.Threshold() - reserveTokens
	if threshold < 1 {
		threshold = 1
	}

	result.PreTokens = CountTokensWithCalibration(messages)

	if result.PreTokens <= threshold {
		result.PostTokens = result.PreTokens
		return messages, result, nil
	}

	// Layer 1: Micro-compact (clear old tool results)
	msgs := c.microCompact(messages)
	tokens := CountTokensWithCalibration(msgs)
	if tokens <= threshold {
		result.Layer = "micro_compact"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	// Layer 2: Tool result compression (per-result token cap)
	msgs = c.compressToolResults(msgs)
	tokens = CountTokensWithCalibration(msgs)
	if tokens <= threshold {
		result.Layer = "tool_result_compression"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	// Layer 3: LLM compact (structured summary via API call). Conversation and
	// thread context are never locally folded or truncated; only tool results
	// may be locally cleared/compressed before this point.
	c.mu.Lock()
	failures := c.consecutiveFailures
	c.mu.Unlock()
	if failures >= MaxConsecutiveCompactFailures {
		result.Layer = "circuit_breaker"
		result.PostTokens = tokens
		result.CircuitBreakerHit = true
		return msgs, result, nil
	}

	if c.LLMClient == nil {
		// No LLM client configured — cannot do Layer 4.
		result.Layer = "no_llm_client"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	model := c.CompactModel
	summary, err := GenerateCompactSummary(ctx, c.LLMClient, model, msgs, "")
	if err != nil {
		c.mu.Lock()
		c.consecutiveFailures++
		c.mu.Unlock()
		result.Layer = "llm_compact_failed"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	// Reset circuit breaker on success.
	c.mu.Lock()
	c.consecutiveFailures = 0
	c.mu.Unlock()

	// Replace messages with boundary + summary + recent messages.
	compacted := c.applyCompactBoundary(msgs, summary)
	result.Layer = "llm_compact"
	result.PostTokens = CountTokensWithCalibration(compacted)
	result.Summary = summary
	return compacted, result, nil
}

// CompactForce runs all compression layers regardless of token threshold.
// Used reactively when the provider rejects a request for exceeding context limits.
func (c *Compactor) CompactForce(ctx context.Context, messages []llm.Message) ([]llm.Message, *CompactResult, error) {
	if len(messages) == 0 {
		return messages, nil, nil
	}

	result := &CompactResult{PreTokens: CountTokensWithCalibration(messages)}
	msgs := c.microCompact(messages)
	msgs = c.compressToolResults(msgs)
	tokens := CountTokensWithCalibration(msgs)

	if c.LLMClient == nil {
		result.Layer = "force_no_llm_client"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	c.mu.Lock()
	failures := c.consecutiveFailures
	c.mu.Unlock()
	if failures >= MaxConsecutiveCompactFailures {
		result.Layer = "force_circuit_breaker"
		result.PostTokens = tokens
		result.CircuitBreakerHit = true
		return msgs, result, nil
	}

	model := c.CompactModel
	summary, err := GenerateCompactSummary(ctx, c.LLMClient, model, msgs, "")
	if err != nil {
		c.mu.Lock()
		c.consecutiveFailures++
		c.mu.Unlock()
		result.Layer = "force_llm_compact_failed"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	c.mu.Lock()
	c.consecutiveFailures = 0
	c.mu.Unlock()

	compacted := c.applyCompactBoundary(msgs, summary)
	result.Layer = "force_llm_compact"
	result.PostTokens = CountTokensWithCalibration(compacted)
	result.Summary = summary
	return compacted, result, nil
}

// RecordCompactSuccess resets the circuit breaker.
func (c *Compactor) RecordCompactSuccess() {
	c.mu.Lock()
	c.consecutiveFailures = 0
	c.mu.Unlock()
}

// --- Layer 1: Micro-compact ---

// microCompact clears old tool results, preserving the most recent N.
// Only tools in ClearableTools are affected. This is the cheapest layer —
// zero API cost, runs every step.
func (c *Compactor) microCompact(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return messages
	}

	keep := c.KeepRecentTools
	if keep <= 0 {
		keep = DefaultKeepRecentTools
	}

	// Count tool results from the end to find the boundary.
	toolCount := 0
	boundary := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" {
			toolCount++
			if toolCount >= keep {
				boundary = i
				break
			}
		}
	}
	if boundary < 0 {
		// Fewer tool results than keep threshold — nothing to clear.
		return messages
	}

	// Copy messages to avoid mutating the original.
	out := make([]llm.Message, len(messages))
	copy(out, messages)

	cleared := false
	for i := 0; i < boundary; i++ {
		if out[i].Role != "tool" {
			continue
		}
		if !c.isClearableTool(out[i].Name) {
			continue
		}
		if len(out[i].Content) <= len(ToolResultClearedMsg) {
			continue
		}
		out[i].Content = ToolResultClearedMsg
		cleared = true
	}

	if !cleared {
		return messages
	}
	return out
}

func (c *Compactor) isClearableTool(name string) bool {
	if len(c.ClearableTools) == 0 {
		// If no explicit list, all tools are clearable (backward compat).
		return true
	}
	return c.ClearableTools[name]
}

// --- Layer 2: Tool result compression ---

// compressToolResults caps each tool result to MaxToolResultTokens, using
// head+tail truncation to preserve the most informative parts.
func (c *Compactor) compressToolResults(messages []llm.Message) []llm.Message {
	maxTokens := c.MaxToolResultTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxToolResultTokens
	}
	maxChars := maxTokens * DefaultBytesPerToken // rough char limit

	out := make([]llm.Message, len(messages))
	copy(out, messages)

	for i := range out {
		if out[i].Role != "tool" {
			continue
		}
		content := out[i].Content
		if len([]rune(content)) <= maxChars {
			continue
		}
		out[i].Content = truncateHeadTail(content, maxChars)
	}
	return out
}

// --- Layer 3: LLM compact ---

// applyCompactBoundary replaces the message history with:
// [system] + [compact summary as user message] + [most recent N messages]
func (c *Compactor) applyCompactBoundary(messages []llm.Message, summary string) []llm.Message {
	// Keep system prompt and the most recent conversation turns.
	var system *llm.Message
	var recent []llm.Message
	for i := range messages {
		if messages[i].Role == "system" && system == nil {
			system = &messages[i]
		}
	}

	// Keep recent messages that fit in ~30% of the threshold.
	tokenBudget := c.Threshold() * 30 / 100
	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		tokens := estimateMessageTokens(&messages[i])
		if accumulated+tokens > tokenBudget && len(recent) > 0 {
			break
		}
		if messages[i].Role == "system" {
			continue // system already handled
		}
		accumulated += tokens
		recent = append([]llm.Message{messages[i]}, recent...)
	}

	// Build the compacted message list.
	result := make([]llm.Message, 0, 2+len(recent))
	if system != nil {
		result = append(result, *system)
	}

	// Insert the compact summary as a user message.
	result = append(result, llm.Message{
		Role:    "user",
		Content: FormatCompactUserMessage(summary),
	})
	result = append(result, recent...)

	return repairToolPairing(result)
}

// repairToolPairing ensures no orphaned tool results exist (a tool_result
// without a preceding assistant message with the matching tool_use).
func repairToolPairing(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return messages
	}

	toolCallIDs := map[string]bool{}
	toolResultIDs := map[string]bool{}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				toolCallIDs[tc.ID] = true
			}
			continue
		}
		if msg.Role == "tool" {
			toolResultIDs[msg.ToolCallID] = true
		}
	}

	// Remove orphaned tool results.
	result := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "tool" && !toolCallIDs[msg.ToolCallID] {
			continue // orphaned tool result
		}
		// Remove tool_use IDs that have no matching tool_result.
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			kept := make([]llm.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				if toolResultIDs[tc.ID] {
					kept = append(kept, tc)
				}
			}
			if len(kept) == 0 && strings.TrimSpace(msg.Content) == "" {
				continue // empty assistant with no valid tool calls
			}
			msg.ToolCalls = kept
		}
		result = append(result, msg)
	}
	return result
}

// truncateHeadTail keeps the head and tail of a string, inserting a marker
// in the middle. This preserves the most informative parts (beginning = setup,
// end = result/conclusion).
func truncateHeadTail(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return string(runes)
	}
	marker := "\n...[middle truncated to fit token budget]...\n"
	markerRunes := len([]rune(marker))
	if maxRunes <= markerRunes+200 {
		return strings.TrimSpace(string(runes[:maxRunes])) + "\n...[truncated]"
	}
	keep := maxRunes - markerRunes
	head := keep / 2
	tail := keep - head
	return strings.TrimSpace(string(runes[:head])) +
		marker +
		strings.TrimSpace(string(runes[len(runes)-tail:]))
}
