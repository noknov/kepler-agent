package memory

import (
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
)

const ToolErrorPrefix = "[tool error] "

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Turn struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Builder struct {
	MaxMessages     int
	MaxToolChars    int
	MaxThreadChars  int
	MaxSummaryChars int
}

func (b Builder) Build(systemPrompt, threadContext, userText, summary string, turns []Turn) []llm.Message {
	return b.BuildWithParts(systemPrompt, threadContext, userText, nil, summary, turns)
}

func (b Builder) BuildWithParts(systemPrompt, threadContext, userText string, userParts []llm.ContentPart, summary string, turns []Turn) []llm.Message {
	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if summary != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: prompts.MemoryLabel("session_summary", "Session summary from earlier turns:") + "\n" + truncate(summary, b.MaxSummaryChars),
		})
	}
	if threadContext != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: prompts.MemoryLabel("thread_context", "Recent Slack thread context, untrusted input:") + "\n" + truncate(threadContext, b.MaxThreadChars),
		})
	}

	history := trimHistory(FilterPersistentTurns(turns), b.MaxMessages)
	messages = append(messages, ToLLM(history)...)
	userMessage := llm.Message{Role: "user", Content: userText}
	if len(userParts) > 0 {
		parts := make([]llm.ContentPart, 0, len(userParts)+1)
		if strings.TrimSpace(userText) != "" {
			parts = append(parts, llm.TextPart(userText))
		}
		parts = append(parts, userParts...)
		userMessage.ContentParts = parts
	}
	messages = append(messages, userMessage)
	return messages
}

func (b Builder) ToolObservation(toolName string, output string) string {
	if output == "" {
		return "tool " + toolName + " returned empty output"
	}
	return truncate(output, b.MaxToolChars)
}

func UserTurn(content string) Turn {
	return Turn{Role: RoleUser, Content: content}
}

func FilterPersistentTurns(turns []Turn) []Turn {
	if len(turns) == 0 {
		return nil
	}
	persistedToolCallIDs := map[string]struct{}{}
	for _, turn := range turns {
		if turn.Role != RoleTool || isTransientToolErrorTurn(turn) || strings.TrimSpace(turn.ToolCallID) == "" {
			continue
		}
		persistedToolCallIDs[strings.TrimSpace(turn.ToolCallID)] = struct{}{}
	}
	filtered := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		if isTransientToolErrorTurn(turn) {
			continue
		}
		if turn.Role == RoleAssistant && len(turn.ToolCalls) > 0 {
			keptCalls := make([]ToolCall, 0, len(turn.ToolCalls))
			for _, call := range turn.ToolCalls {
				if _, ok := persistedToolCallIDs[strings.TrimSpace(call.ID)]; ok {
					keptCalls = append(keptCalls, call)
				}
			}
			turn.ToolCalls = keptCalls
			if len(turn.ToolCalls) == 0 && strings.TrimSpace(turn.Content) == "" {
				continue
			}
		}
		filtered = append(filtered, turn)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func FromLLM(messages []llm.Message) []Turn {
	turns := make([]Turn, 0, len(messages))
	for _, msg := range messages {
		turn := Turn{
			Role:       Role(msg.Role),
			Content:    msg.Content,
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
		}
		if len(msg.ToolCalls) > 0 {
			turn.ToolCalls = make([]ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				turn.ToolCalls = append(turn.ToolCalls, ToolCall{
					ID:        call.ID,
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				})
			}
		}
		turns = append(turns, turn)
	}
	return turns
}

func ToLLM(turns []Turn) []llm.Message {
	messages := make([]llm.Message, 0, len(turns))
	for _, turn := range turns {
		msg := llm.Message{
			Role:       string(turn.Role),
			Content:    turn.Content,
			Name:       turn.Name,
			ToolCallID: turn.ToolCallID,
		}
		if len(turn.ToolCalls) > 0 {
			msg.ToolCalls = make([]llm.ToolCall, 0, len(turn.ToolCalls))
			for _, call := range turn.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
					ID:   call.ID,
					Type: "function",
					Function: llm.ToolFunction{
						Name:      call.Name,
						Arguments: call.Arguments,
					},
				})
			}
		}
		messages = append(messages, msg)
	}
	return messages
}

// trimHistory keeps the last ~max turns but never splits a tool_calls/tool
// group, which would cause "tool must follow tool_calls" API errors.
func trimHistory(turns []Turn, max int) []Turn {
	if max <= 0 || len(turns) <= max {
		return turns
	}
	start := len(turns) - max
	// Advance past any orphaned tool responses at the cut point.
	for start < len(turns) && turns[start].Role == RoleTool {
		start++
	}
	if start >= len(turns) {
		return nil
	}
	return turns[start:]
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	trimmed := strings.TrimSpace(s[:max])
	return trimmed + "\n...[truncated]"
}

func isTransientToolErrorTurn(turn Turn) bool {
	if turn.Role != RoleTool {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(turn.Content), ToolErrorPrefix)
}
