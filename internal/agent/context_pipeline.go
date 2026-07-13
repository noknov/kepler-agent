package agent

import (
	"context"
	"sort"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
)

// aggregateToolResultBudget is the total character limit for all tool results
// in the most recent tool-call batch (the group of tool messages produced by
// the last assistant message with tool_calls). Mirrors claude-code's
// MAX_TOOL_RESULTS_PER_MESSAGE_CHARS.
//
// Per-result spill handles individual giants (>15K chars); this aggregate cap
// handles many moderate-sized results (e.g. 8 tools × 10K = 80K chars) that
// individually pass the per-result threshold but collectively blow out the
// context window.
const aggregateToolResultBudget = 60_000

// prepareMessagesForQuery mirrors Claude Code's per-iteration context pipeline:
// first project large tool results into persisted references, then run the
// compaction stack, and only then normalize provider-facing message shape.
func (r Runner) prepareMessagesForQuery(ctx context.Context, messages []llm.Message, req Request, toolSpecs []llm.ToolSpec) []llm.Message {
	messages = r.applyToolResultBudget(messages, req)
	messages = r.compactMessages(ctx, messages, memory.EstimateToolSpecTokens(toolSpecs))
	return memory.PrepareForLLM(messages)
}

func (r Runner) prepareMessagesForOverflowRetry(ctx context.Context, messages []llm.Message, req Request) []llm.Message {
	messages = r.applyToolResultBudget(messages, req)
	messages = r.compactMessagesAggressive(ctx, messages)
	return memory.PrepareForLLM(messages)
}

// applyToolResultBudget enforces two complementary limits:
//
//  1. Per-result: any single tool result exceeding maxToolResultChars is spilled
//     to disk and replaced with a preview + read-back pointer.
//
//  2. Aggregate: after per-result spill, if the total chars of all tool results
//     in the most recent batch still exceeds aggregateToolResultBudget, the
//     largest remaining results are spilled one by one until we are under budget.
//     This prevents many moderate-sized results from collectively blowing out
//     the context window even though each individually passes the per-result cap.
func (r Runner) applyToolResultBudget(messages []llm.Message, req Request) []llm.Message {
	if len(messages) == 0 {
		return messages
	}

	// Pass 1: per-result spill for any historical large results that were not
	// already persisted at tool execution time.
	var out []llm.Message
	for i, msg := range messages {
		if msg.Role != "tool" || !shouldPersistToolResult(msg.Content) {
			continue
		}
		if req.ContentReplacementState != nil {
			if replacement, ok := req.ContentReplacementState.Replacements[msg.ToolCallID]; ok {
				if out == nil {
					out = make([]llm.Message, len(messages))
					copy(out, messages)
				}
				out[i].Content = replacement
				continue
			}
			if req.ContentReplacementState.Seen[msg.ToolCallID] {
				continue
			}
		}
		if out == nil {
			out = make([]llm.Message, len(messages))
			copy(out, messages)
		}
		replacement := maybeSpillResult(spillRunID(req.RunID), msg.Name, msg.ToolCallID, msg.Content)
		out[i].Content = replacement
		if req.ContentReplacementState != nil && replacement != msg.Content {
			req.ContentReplacementState.AddReplacement(msg.ToolCallID, replacement)
		}
	}
	if out != nil {
		messages = out
		out = nil
	}
	if req.ContentReplacementState != nil {
		return r.applyStatefulToolResultBudget(messages, req)
	}

	// Pass 2: aggregate budget check on the most recent tool-call batch.
	// Locate the last assistant message that issued tool calls; all tool
	// messages after it form the "current batch".
	batchStart := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			batchStart = i + 1
			break
		}
	}
	if batchStart < 0 {
		return messages
	}

	// Sum the char lengths of all tool results in this batch.
	type indexedLen struct {
		idx int
		n   int
	}
	var batch []indexedLen
	total := 0
	for i := batchStart; i < len(messages); i++ {
		if messages[i].Role != "tool" {
			continue
		}
		n := len(messages[i].Content)
		batch = append(batch, indexedLen{idx: i, n: n})
		total += n
	}
	if total <= aggregateToolResultBudget {
		return messages
	}

	// Spill the largest results first until we fit within the budget.
	sort.Slice(batch, func(i, j int) bool { return batch[i].n > batch[j].n })
	out = make([]llm.Message, len(messages))
	copy(out, messages)
	spilled := 0
	for _, entry := range batch {
		if total <= aggregateToolResultBudget {
			break
		}
		msg := out[entry.idx]
		if strings.HasPrefix(strings.TrimSpace(msg.Content), "<persisted-output>") {
			continue // already spilled by pass 1
		}
		spilled++
		newContent := maybeSpillResult(spillRunID(req.RunID), msg.Name, msg.ToolCallID, msg.Content)
		total -= entry.n - len(newContent)
		out[entry.idx].Content = newContent
	}
	if spilled > 0 {
		r.observeEvent("aggregate_budget_applied", map[string]any{"spilled": spilled, "batch_size": len(batch)})
	}
	return out
}

func (r Runner) applyStatefulToolResultBudget(messages []llm.Message, req Request) []llm.Message {
	state := req.ContentReplacementState
	if state == nil {
		return messages
	}
	out := make([]llm.Message, len(messages))
	copy(out, messages)

	// Re-apply prior replacements byte-for-byte and collect the latest fresh
	// tool-result batch. This is the Go analogue of Claude Code's
	// ContentReplacementState: once a tool_call_id is seen, its fate is frozen.
	type candidate struct {
		idx int
		n   int
	}
	var batch []candidate
	total := 0
	batchStart := -1
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == "assistant" && len(out[i].ToolCalls) > 0 {
			batchStart = i + 1
			break
		}
	}
	if batchStart < 0 {
		for i := range out {
			if out[i].Role == "tool" {
				if replacement, ok := state.Replacements[out[i].ToolCallID]; ok {
					out[i].Content = replacement
				}
				state.MarkSeen(out[i].ToolCallID)
			}
		}
		return out
	}

	for i := range out {
		if out[i].Role != "tool" {
			continue
		}
		if replacement, ok := state.Replacements[out[i].ToolCallID]; ok {
			out[i].Content = replacement
		}
		if i < batchStart {
			state.MarkSeen(out[i].ToolCallID)
			continue
		}
		n := len(out[i].Content)
		total += n
		if state.Seen[out[i].ToolCallID] {
			continue
		}
		batch = append(batch, candidate{idx: i, n: n})
	}
	if total <= aggregateToolResultBudget {
		for _, entry := range batch {
			state.MarkSeen(out[entry.idx].ToolCallID)
		}
		return out
	}

	sort.Slice(batch, func(i, j int) bool { return batch[i].n > batch[j].n })
	spilled := 0
	for _, entry := range batch {
		msg := out[entry.idx]
		if total <= aggregateToolResultBudget {
			state.MarkSeen(msg.ToolCallID)
			continue
		}
		replacement := msg.Content
		if !strings.HasPrefix(strings.TrimSpace(replacement), "<persisted-output>") {
			replacement = maybeSpillResult(spillRunID(req.RunID), msg.Name, msg.ToolCallID, msg.Content)
		}
		out[entry.idx].Content = replacement
		state.AddReplacement(msg.ToolCallID, replacement)
		total -= entry.n - len(replacement)
		spilled++
	}
	for _, entry := range batch {
		state.MarkSeen(out[entry.idx].ToolCallID)
	}
	if spilled > 0 {
		r.observeEvent("stateful_aggregate_budget_applied", map[string]any{"spilled": spilled, "batch_size": len(batch)})
	}
	return out
}

func shouldPersistToolResult(content string) bool {
	if len(content) <= maxToolResultChars {
		return false
	}
	trimmed := strings.TrimSpace(content)
	return !strings.HasPrefix(trimmed, "<persisted-output>")
}
