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
	Usage      *llm.Usage `json:"usage,omitempty"`
}

type Builder struct {
	// MaxContextTokens is the total context window token budget. Current tool
	// results are not locally truncated here; large outputs are persisted by
	// the agent spill layer so the model can read them back in complete slices.
	MaxContextTokens int
}

func (b Builder) Build(systemPrompt, threadContext, userText, summary string, turns []Turn) []llm.Message {
	return b.BuildWithParts(systemPrompt, threadContext, userText, nil, summary, turns)
}

func (b Builder) BuildWithParts(systemPrompt, threadContext, userText string, userParts []llm.ContentPart, summary string, turns []Turn) []llm.Message {
	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if summary != "" {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: prompts.MemoryLabel("session_summary", "") + "\n<session_summary>\n" + strings.TrimSpace(summary) + "\n</session_summary>",
		})
	}
	if threadContext != "" {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: prompts.MemoryLabel("thread_context", "") + "\n<slack_thread_context>\n" + threadContext + "\n</slack_thread_context>",
		})
	}

	userMessage := llm.Message{Role: "user", Content: userText}
	if len(userParts) > 0 {
		parts := make([]llm.ContentPart, 0, len(userParts)+1)
		if strings.TrimSpace(userText) != "" {
			parts = append(parts, llm.TextPart(userText))
		}
		parts = append(parts, userParts...)
		userMessage.ContentParts = parts
	}
	messages = append(messages, ToLLM(FilterPersistentTurns(turns))...)
	messages = append(messages, userMessage)
	return messages
}

func (b Builder) ToolObservation(toolName string, output string) string {
	if output == "" {
		return "tool " + toolName + " returned empty output"
	}
	if toolName == "delegate-run" {
		output = prompts.MemoryLabel("delegate_provenance", "") + output
	}
	if toolName == "explore-code" {
		output = prompts.MemoryLabel("explore_provenance", "") + output
	}
	return "<evidence source=\"" + toolName + "\">\n" + output + "\n</evidence>"
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
			Usage:      msg.Usage,
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
			Usage:      turn.Usage,
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

func isTransientToolErrorTurn(turn Turn) bool {
	if turn.Role != RoleTool {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(turn.Content), ToolErrorPrefix)
}
