package memory

import (
	"sort"
	"strconv"
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
	MaxMessages     int
	MaxToolChars    int
	MaxThreadChars  int
	MaxSummaryChars int
	// MaxContextTokens is the total context window token budget.
	// When set, tool results and thread context are budgeted in tokens
	// (using RoughTokenEstimate) in addition to the char-based limits.
	MaxContextTokens int
}

type threadExcerpt struct {
	index int
	text  string
	score int
}

func (b Builder) Build(systemPrompt, threadContext, userText, summary string, turns []Turn) []llm.Message {
	return b.BuildWithParts(systemPrompt, threadContext, userText, nil, summary, turns)
}

func (b Builder) BuildWithParts(systemPrompt, threadContext, userText string, userParts []llm.ContentPart, summary string, turns []Turn) []llm.Message {
	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if summary != "" {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: prompts.MemoryLabel("session_summary", "") + "\n<session_summary>\n" + truncate(summary, b.MaxSummaryChars) + "\n</session_summary>",
		})
	}
	if threadContext != "" {
		threadContext = CompressThreadContext(threadContext, b.MaxThreadChars)
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: prompts.MemoryLabel("thread_context", "") + "\n<slack_thread_context>\n" + threadContext + "\n</slack_thread_context>",
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
	if toolName == "delegate-run" {
		output = prompts.MemoryLabel("delegate_provenance", "") + output
	}
	if toolName == "explore-code" {
		output = prompts.MemoryLabel("explore_provenance", "") + output
	}
	// Apply token-aware truncation: use the smaller of char and token budgets.
	maxChars := b.MaxToolChars
	if b.MaxContextTokens > 0 {
		// Reserve at most 25% of context window for a single tool result.
		tokenBudget := b.MaxContextTokens / 4
		charBudgetFromTokens := tokenBudget * DefaultBytesPerToken
		if maxChars <= 0 || charBudgetFromTokens < maxChars {
			maxChars = charBudgetFromTokens
		}
	}
	return "<evidence source=\"" + toolName + "\">\n" + truncate(output, maxChars) + "\n</evidence>"
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

// trimHistory keeps the last ~max turns but never splits a tool_calls/tool
// group, which would cause "tool must follow tool_calls" API errors.
// When MaxContextTokens is set on the Builder, it also respects the token budget.
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

func CompressThreadContext(context string, budget int) string {
	context = strings.TrimSpace(context)
	if budget <= 0 || len(context) <= budget {
		return context
	}
	lines := compactLines(strings.Split(context, "\n"))
	if len(lines) == 0 {
		return ""
	}
	sections := []string{
		prompts.PromptText("thread_compressed_header", ""),
		prompts.PromptText("thread_compressed_note", ""),
	}
	headCount, tailCount := 3, 8
	headEnd := min(headCount, len(lines))
	tailStart := max(headEnd, len(lines)-tailCount)
	sections = append(sections, prompts.PromptText("thread_earliest_messages", ""))
	sections = append(sections, numberedLines(lines[:headEnd], 1, 700)...)
	if tailStart > headEnd {
		middle := lines[headEnd:tailStart]
		sections = append(sections, prompts.PromptText("thread_middle_summary", ""))
		sections = append(sections, summarizeMiddleThread(middle, headEnd+1, budget/3)...)
	}
	if tailStart < len(lines) {
		sections = append(sections, prompts.PromptText("thread_recent_messages", ""))
		sections = append(sections, numberedLines(lines[tailStart:], tailStart+1, 900)...)
	}
	compressed := strings.Join(sections, "\n")
	if len(compressed) <= budget {
		return compressed
	}
	return truncate(compressed, budget)
}

func compactLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func numberedLines(lines []string, start, maxLineChars int) []string {
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		out = append(out, "- msg "+strconv.Itoa(start+i)+": "+truncateInline(line, maxLineChars))
	}
	return out
}

func summarizeMiddleThread(lines []string, start, budget int) []string {
	if len(lines) == 0 {
		return nil
	}
	if budget < 600 {
		budget = 600
	}
	out := []string{"- omitted " + strconv.Itoa(len(lines)) + " middle message(s), msg " + strconv.Itoa(start) + "-" + strconv.Itoa(start+len(lines)-1) + "."}
	for _, item := range selectThreadExcerpts(lines, start, budget-len(out[0])) {
		out = append(out, item)
	}
	return out
}

func selectThreadExcerpts(lines []string, start, budget int) []string {
	scored := make([]threadExcerpt, 0, len(lines))
	for i, line := range lines {
		score := threadLineScore(line)
		if score == 0 {
			continue
		}
		scored = append(scored, threadExcerpt{index: i, text: line, score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		return betterThreadExcerpt(scored[i], scored[j])
	})
	out := make([]string, 0, min(6, len(scored)))
	used := 0
	for _, item := range scored {
		line := "- relevant msg " + strconv.Itoa(start+item.index) + ": " + truncateInline(item.text, 500)
		if used+len(line) > budget && len(out) > 0 {
			break
		}
		out = append(out, line)
		used += len(line)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func threadLineScore(line string) int {
	lower := strings.ToLower(line)
	score := 0
	for _, keyword := range []string{
		"error", "exception", "failed", "failure", "timeout", "panic", "trace", "stack",
		"root cause", "原因", "失败", "错误", "异常", "超时", "堆栈",
		"decision", "decided", "should", "must", "需要", "应该", "决定",
		"api/", "http", "status=", "id=", "commit", "branch", "deploy",
	} {
		if strings.Contains(lower, keyword) {
			score += 3
		}
	}
	if strings.Contains(line, "?") || strings.Contains(line, "？") {
		score += 2
	}
	if strings.Contains(line, "```") || strings.Contains(line, "`") {
		score++
	}
	return score
}

func betterThreadExcerpt(a, b threadExcerpt) bool {
	if a.score == b.score {
		return a.index > b.index
	}
	return a.score > b.score
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	marker := "\n...[middle truncated to fit context budget]...\n"
	markerRunes := len([]rune(marker))
	if max <= markerRunes+200 {
		return strings.TrimSpace(string(runes[:max])) + "\n...[truncated]"
	}
	keep := max - markerRunes
	head := keep / 2
	tail := keep - head
	return strings.TrimSpace(string(runes[:head])) + marker + strings.TrimSpace(string(runes[len(runes)-tail:]))
}

func truncateInline(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + "...[truncated]"
}

func isTransientToolErrorTurn(turn Turn) bool {
	if turn.Role != RoleTool {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(turn.Content), ToolErrorPrefix)
}
