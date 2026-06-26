package memory

import (
	"fmt"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
)

// PrepareForLLM normalizes conversation history before sending it back to
// chat APIs. Modeled after Claude Code's normalizeMessagesForAPI — handles
// tool adjacency repair, duplicate ID dedup, orphaned message cleanup,
// and provider-specific field normalization.
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
		out[i].ToolCalls = NormalizeToolCalls(out[i].ToolCalls)
	}

	out = filterEmptyAssistantMessages(out)
	out = stripTrailingReasoning(out)
	out = deduplicateToolCallIDs(out)
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

// filterEmptyAssistantMessages removes assistant messages that have no
// content, no tool calls, and no reasoning — these cause 400 errors on
// most providers.
func filterEmptyAssistantMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "assistant" &&
			strings.TrimSpace(msg.Content) == "" &&
			strings.TrimSpace(msg.ReasoningContent) == "" &&
			len(msg.ToolCalls) == 0 {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// stripTrailingReasoning clears ReasoningContent from the last assistant
// message if it has no text content and no tool calls. DeepSeek/OpenAI
// providers can reject messages that end with only reasoning.
func stripTrailingReasoning(messages []llm.Message) []llm.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		if strings.TrimSpace(messages[i].Content) == "" &&
			len(messages[i].ToolCalls) == 0 &&
			strings.TrimSpace(messages[i].ReasoningContent) != "" {
			messages[i].ReasoningContent = ""
			if strings.TrimSpace(messages[i].Content) == "" {
				// Remove the now-empty message entirely.
				messages = append(messages[:i], messages[i+1:]...)
			}
		}
		break
	}
	return messages
}

// deduplicateToolCallIDs ensures no two tool_calls across the conversation
// share the same ID. Duplicates cause 400 errors on OpenAI-compatible APIs.
func deduplicateToolCallIDs(messages []llm.Message) []llm.Message {
	seen := map[string]bool{}
	counter := 0
	for i := range messages {
		if messages[i].Role != "assistant" || len(messages[i].ToolCalls) == 0 {
			continue
		}
		for j := range messages[i].ToolCalls {
			id := messages[i].ToolCalls[j].ID
			if seen[id] {
				counter++
				newID := fmt.Sprintf("%s_dedup_%d", id, counter)
				oldID := id
				messages[i].ToolCalls[j].ID = newID
				// Also fix the corresponding tool result.
				for k := i + 1; k < len(messages); k++ {
					if messages[k].Role == "tool" && messages[k].ToolCallID == oldID {
						messages[k].ToolCallID = newID
						break
					}
				}
			}
			seen[messages[i].ToolCalls[j].ID] = true
		}
	}
	return messages
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
