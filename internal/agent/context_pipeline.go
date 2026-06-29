package agent

import (
	"context"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
)

// prepareMessagesForQuery mirrors Claude Code's per-iteration context pipeline:
// first project large tool results into persisted references, then run the
// compaction stack, and only then normalize provider-facing message shape.
func (r Runner) prepareMessagesForQuery(ctx context.Context, messages []llm.Message, req Request) []llm.Message {
	messages = r.applyToolResultBudget(messages, req)
	messages = r.compactMessages(ctx, messages)
	return memory.PrepareForLLM(messages)
}

func (r Runner) prepareMessagesForOverflowRetry(ctx context.Context, messages []llm.Message, req Request) []llm.Message {
	messages = r.applyToolResultBudget(messages, req)
	messages = r.compactMessagesAggressive(ctx, messages)
	return memory.PrepareForLLM(messages)
}

func (r Runner) applyToolResultBudget(messages []llm.Message, req Request) []llm.Message {
	if len(messages) == 0 {
		return messages
	}
	var out []llm.Message
	for i, msg := range messages {
		if msg.Role != "tool" || !shouldPersistToolResult(msg.Content) {
			continue
		}
		if out == nil {
			out = make([]llm.Message, len(messages))
			copy(out, messages)
		}
		out[i].Content = maybeSpillResult(spillRunID(req.RunID), msg.Name, msg.ToolCallID, msg.Content)
	}
	if out == nil {
		return messages
	}
	r.observeEvent("tool_result_budget_applied", map[string]any{"messages": len(out)})
	return out
}

func shouldPersistToolResult(content string) bool {
	if len(content) <= maxToolResultChars {
		return false
	}
	trimmed := strings.TrimSpace(content)
	return !strings.HasPrefix(trimmed, "<persisted-output>")
}
