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

// CompactIfNeeded runs all 4 compression layers as needed.
// It is called between conversation turns (not every step) to manage
// persistent session context.
func (c *Compactor) CompactIfNeeded(ctx context.Context, messages []llm.Message) ([]llm.Message, *CompactResult, error) {
	if len(messages) == 0 {
		return messages, nil, nil
	}

	result := &CompactResult{}
	result.PreTokens = CountTokensWithCalibration(messages)

	// Check if we're under threshold — no compression needed.
	if result.PreTokens <= c.Threshold() {
		result.PostTokens = result.PreTokens
		return messages, result, nil
	}

	// Layer 1: Micro-compact (clear old tool results)
	msgs := c.microCompact(messages)
	tokens := CountTokensWithCalibration(msgs)
	if tokens <= c.Threshold() {
		result.Layer = "micro_compact"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	// Layer 2: Tool result compression (per-result token cap)
	msgs = c.compressToolResults(msgs)
	tokens = CountTokensWithCalibration(msgs)
	if tokens <= c.Threshold() {
		result.Layer = "tool_result_compression"
		result.PostTokens = tokens
		return msgs, result, nil
	}

	// Layer 3: History folding (trim older messages, keep important ones)
	msgs, foldedSummary := c.foldHistory(msgs)
	tokens = CountTokensWithCalibration(msgs)
	if tokens <= c.Threshold() {
		result.Layer = "history_folding"
		result.PostTokens = tokens
		result.Summary = foldedSummary
		return msgs, result, nil
	}

	// Layer 4: LLM compact (structured summary via API call)
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
		return msgs, nil, err
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

// --- Layer 3: History folding ---

// foldHistory trims older messages to fit within the token budget while
// preserving important ones (errors, decisions, user messages). Returns
// the trimmed messages and a brief summary of what was folded.
func (c *Compactor) foldHistory(messages []llm.Message) ([]llm.Message, string) {
	threshold := c.Threshold()

	// Find the system prompt (always keep it).
	systemIdx := -1
	for i, msg := range messages {
		if msg.Role == "system" {
			systemIdx = i
			break
		}
	}

	// Score each non-system message by importance.
	type scored struct {
		index int
		score int
	}
	scoredMessages := make([]scored, 0, len(messages))
	for i, msg := range messages {
		if i == systemIdx {
			continue
		}
		scoredMessages = append(scoredMessages, scored{
			index: i,
			score: messageImportanceScore(&msg),
		})
	}

	// Always keep the most recent messages (last 25% of the list).
	keepRecentFrom := len(messages) - len(messages)/4
	if keepRecentFrom < 2 {
		keepRecentFrom = 2
	}

	// Build the kept set: system + recent + high-importance older messages.
	keepSet := make(map[int]bool)
	if systemIdx >= 0 {
		keepSet[systemIdx] = true
	}
	for i := keepRecentFrom; i < len(messages); i++ {
		keepSet[i] = true
	}

	// Fill remaining budget with highest-scored older messages.
	currentTokens := 0
	for i := range messages {
		if keepSet[i] {
			currentTokens += estimateMessageTokens(&messages[i])
		}
	}

	// Sort older messages by score (descending), then add until budget is full.
	var older []scoredIndex
	for _, s := range scoredMessages {
		if s.index < keepRecentFrom {
			older = append(older, scoredIndex{s.index, s.score})
		}
	}
	// Sort by score descending, then by index descending (prefer later).
	sortByScoreDesc(older)

	for _, om := range older {
		msgTokens := estimateMessageTokens(&messages[om.index])
		if currentTokens+msgTokens > threshold {
			continue
		}
		keepSet[om.index] = true
		currentTokens += msgTokens
	}

	// Build folded messages and summary of removed ones.
	var folded []llm.Message
	var summaryParts []string
	for i := range messages {
		if keepSet[i] {
			folded = append(folded, messages[i])
		} else {
			desc := describeFoldedMessage(&messages[i])
			if desc != "" {
				summaryParts = append(summaryParts, desc)
			}
		}
	}

	// Ensure tool_use/tool_result pairing is not broken.
	folded = repairToolPairing(folded)

	summary := ""
	if len(summaryParts) > 0 {
		summary = "Folded conversation history:\n" + strings.Join(summaryParts, "\n")
	}
	return folded, summary
}

// --- Layer 4: LLM compact ---

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

	return result
}

// --- helpers ---

type scoredIndex struct {
	index int
	score int
}

// messageImportanceScore rates how important a message is for context retention.
// Higher scores = more important = less likely to be folded away.
func messageImportanceScore(msg *llm.Message) int {
	score := 0
	content := strings.ToLower(msg.Content)

	// User messages are always high priority.
	if msg.Role == "user" {
		score += 10
	}

	// Error/failure signals
	for _, keyword := range []string{
		"error", "exception", "failed", "failure", "timeout", "panic",
	} {
		if strings.Contains(content, keyword) {
			score += 5
		}
	}

	// Decision signals
	for _, keyword := range []string{
		"decision", "decided", "should", "must", "important",
	} {
		if strings.Contains(content, keyword) {
			score += 3
		}
	}

	// Tool results that contain findings
	if msg.Role == "tool" {
		if len(msg.Content) > 100 {
			score += 2 // non-trivial tool result
		}
	}

	// Assistant messages with tool calls (preserve structure)
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		score += 3
	}

	// Recency bonus: handled by the caller via keepRecentFrom
	return score
}

// describeFoldedMessage generates a one-line description of a folded message.
func describeFoldedMessage(msg *llm.Message) string {
	switch msg.Role {
	case "user":
		content := strings.Join(strings.Fields(msg.Content), " ")
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		return "- user: " + content
	case "assistant":
		if len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			return "- assistant: called tools: " + strings.Join(names, ", ")
		}
		content := strings.Join(strings.Fields(msg.Content), " ")
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		return "- assistant: " + content
	case "tool":
		return "- tool:" + msg.Name + " (result cleared)"
	default:
		return ""
	}
}

// repairToolPairing ensures no orphaned tool results exist (a tool_result
// without a preceding assistant message with the matching tool_use).
func repairToolPairing(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return messages
	}

	// Collect all tool_use IDs from assistant messages.
	pendingIDs := map[string]bool{}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				pendingIDs[tc.ID] = true
			}
		}
	}

	// Remove orphaned tool results.
	result := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "tool" && !pendingIDs[msg.ToolCallID] {
			continue // orphaned tool result
		}
		// Remove tool_use IDs that have no matching tool_result.
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Check if all tool calls have results ahead.
			resultIDs := map[string]bool{}
			for j := range messages {
				if messages[j].Role == "tool" {
					resultIDs[messages[j].ToolCallID] = true
				}
			}
			kept := make([]llm.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				if resultIDs[tc.ID] {
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

// sortByScoreDesc sorts messages by score descending, breaking ties
// by index descending (prefer later/more recent messages).
func sortByScoreDesc(msgs []scoredIndex) {
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0; j-- {
			if msgs[j].score > msgs[j-1].score ||
				(msgs[j].score == msgs[j-1].score && msgs[j].index > msgs[j-1].index) {
				msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
			} else {
				break
			}
		}
	}
}
