package llm

import (
	"regexp"
	"strings"
)

var (
	reToolCallBlock      = regexp.MustCompile(`(?is)<tool_call>.*?</tool_call>`)
	reFunctionTag        = regexp.MustCompile(`(?is)<function=[^>]+>.*?</function>`)
	reToolInvocation     = regexp.MustCompile(`(?is)<tool_invocation[^>]*>.*?</tool_invocation>`)
	reToolInvocationSelf = regexp.MustCompile(`(?i)<tool_invocation[^>]*/\s*>`)
	reToolNameBlock      = regexp.MustCompile(`(?is)<tool_name>.*?</tool_name>`)
	reParametersBlock    = regexp.MustCompile(`(?is)<parameters>.*?</parameters>`)
	reToolCallCodeBlock  = regexp.MustCompile("(?s)```[\\w]*\\s*tool_call.*?```")
)

// LooksLikeTextualToolCall reports whether content appears to describe tool invocations
// as markup/text rather than via structured API fields. Used for repair/retry only;
// parsed text is never executed as a tool call.
func LooksLikeTextualToolCall(content string) bool {
	s := strings.ToLower(strings.TrimSpace(content))
	if s == "" {
		return false
	}
	if strings.Contains(s, "<tool_call") || strings.Contains(s, "</tool_call>") {
		return true
	}
	if strings.Contains(s, "<function=") {
		return true
	}
	if strings.Contains(s, "<tool_name>") || strings.Contains(s, "<parameters>") {
		return true
	}
	if strings.Contains(s, "<tool_invocation") || strings.Contains(s, "</tool_invocation>") {
		return true
	}
	if strings.Contains(s, "```") && strings.Contains(s, "tool_call") {
		return true
	}
	return false
}

// NormalizeAssistantMessage trims assistant output and removes textual tool-call
// markup from Content when structured ToolCalls are already present.
func NormalizeAssistantMessage(caps Capabilities, msg Message, _ []ToolSpec) Message {
	msg.Content = strings.TrimSpace(msg.Content)
	if len(msg.ToolCalls) > 0 {
		msg.Content = StripTextualToolCallMarkup(msg.Content)
	}
	_ = caps
	return msg
}

// StripTextualToolCallMarkup removes all textual tool-call markup patterns
// from content, returning whatever plain text remains.
func StripTextualToolCallMarkup(content string) string {
	if content == "" {
		return ""
	}
	content = reToolCallBlock.ReplaceAllString(content, "")
	content = reFunctionTag.ReplaceAllString(content, "")
	content = reToolInvocation.ReplaceAllString(content, "")
	content = reToolInvocationSelf.ReplaceAllString(content, "")
	content = reToolNameBlock.ReplaceAllString(content, "")
	content = reParametersBlock.ReplaceAllString(content, "")
	content = reToolCallCodeBlock.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}
