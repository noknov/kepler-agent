package memory

import (
	"fmt"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
)

// PrepareForLLM removes replay-unsafe assistant fields and repairs
// assistant/tool adjacency before sending history back to chat APIs.
func PrepareForLLM(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Role != "assistant" {
			continue
		}
		out[i].ReasoningContent = ""
		out[i].ToolCalls = NormalizeToolCalls(out[i].ToolCalls)
	}
	return repairToolAdjacency(out)
}

// NormalizeToolCalls removes empty streamed tool-call slots and fills fields
// that OpenAI-compatible providers require when messages are replayed.
func NormalizeToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]llm.ToolCall, 0, len(calls))
	for i, call := range calls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		if strings.TrimSpace(call.Type) == "" {
			call.Type = "function"
		}
		if strings.TrimSpace(call.ID) == "" {
			call.ID = fmt.Sprintf("call_%s_%d", name, i)
		}
		call.Function.Name = name
		out = append(out, call)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func repairToolAdjacency(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == "tool" {
			continue
		}
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			out = append(out, msg)
			continue
		}

		out = append(out, msg)
		for _, tc := range msg.ToolCalls {
			if existing, ok := findToolResultAfter(messages, i, tc.ID); ok {
				out = append(out, existing)
				continue
			}
			out = append(out, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    "[tool error] missing tool result; treating as no output",
			})
		}
	}
	return out
}

func findToolResultAfter(messages []llm.Message, assistantIndex int, toolCallID string) (llm.Message, bool) {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return llm.Message{}, false
	}
	for i := assistantIndex + 1; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == "assistant" || msg.Role == "user" {
			return llm.Message{}, false
		}
		if msg.Role != "tool" || strings.TrimSpace(msg.ToolCallID) != toolCallID {
			continue
		}
		return msg, true
	}
	return llm.Message{}, false
}
