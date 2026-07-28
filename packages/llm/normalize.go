package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	reToolCallBlock      = regexp.MustCompile(`(?is)<tool_call>.*?</tool_call>`)
	reFunctionTag        = regexp.MustCompile(`(?is)<function=[^>]+>.*?</function>`)
	reToolInvocation     = regexp.MustCompile(`(?is)<tool_invocation[^>]*>.*?</tool_invocation>`)
	reToolInvocationSelf = regexp.MustCompile(`(?i)<tool_invocation[^>]*/\s*>`)
	reDSMLToolCalls      = regexp.MustCompile(`(?is)<\s*｜｜DSML｜｜tool_calls\s*>.*?<\s*/\s*｜｜DSML｜｜tool_calls\s*>`)
	reDSMLInvoke         = regexp.MustCompile(`(?is)<\s*｜｜DSML｜｜invoke\b.*?<\s*/\s*｜｜DSML｜｜invoke\s*>`)
	reToolNameBlock      = regexp.MustCompile(`(?is)<tool_name>.*?</tool_name>`)
	reParametersBlock    = regexp.MustCompile(`(?is)<parameters>.*?</parameters>`)
	reToolCallCodeBlock  = regexp.MustCompile("(?s)```[\\w]*\\s*tool_call.*?```")

	// Parsing patterns for extracting structured tool calls from textual markup.
	reParseToolInvocation       = regexp.MustCompile(`(?is)<tool_invocation\s+name="([^"]+)"\s+arguments=([\s\S]*?)(?:/>|</tool_invocation>)`)
	reParseToolInvocationQuoted = regexp.MustCompile(`(?is)<tool_invocation\s+name=["']([^"']+)["']\s+arguments=["']([\s\S]*?)["']\s*(?:/>|>[\s\S]*?</tool_invocation>)`)
	reParseFunctionTag          = regexp.MustCompile(`(?is)<function=([^>]+)>([\s\S]*?)</function>`)
	reParseDSMLInvoke           = regexp.MustCompile(`(?is)<\s*｜｜DSML｜｜invoke\s+name="([^"]+)"[^>]*>([\s\S]*?)<\s*/\s*｜｜DSML｜｜invoke\s*>`)
	reParseToolNameParams       = regexp.MustCompile(`(?is)<tool_name>\s*([^<]+?)\s*</tool_name>\s*<parameters>\s*([\s\S]*?)\s*</parameters>`)
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
	if strings.Contains(s, "dsml") && (strings.Contains(s, "tool_calls") || strings.Contains(s, "invoke")) {
		return true
	}
	if strings.Contains(s, "```") && strings.Contains(s, "tool_call") {
		return true
	}
	return false
}

// MayBecomeTextualToolCall reports whether a stream prefix ends inside a
// possible textual tool-call marker. It is deliberately conservative and is
// used only to delay streaming by a tiny tail buffer.
func MayBecomeTextualToolCall(content string) bool {
	s := strings.ToLower(content)
	tails := []string{
		"<", "<t", "<to", "<too", "<tool", "<tool_", "<tool_c", "<tool_ca", "<tool_cal",
		"<f", "<fu", "<fun", "<func", "<funct", "<functi", "<functio", "<function",
		"<p", "<pa", "<par", "<para", "<param", "<parame", "<paramet", "<paramete", "<parameter",
		"<｜", "<｜｜", "<｜｜d", "<｜｜ds", "<｜｜dsm", "<｜｜dsml",
		"```", "``", "`",
	}
	for _, tail := range tails {
		if strings.HasSuffix(s, tail) {
			return true
		}
	}
	return false
}

// NormalizeAssistantMessage trims assistant output and handles textual tool-call
// markup. When structured ToolCalls are present, strips textual markup from Content.
// When no structured ToolCalls exist but textual markup is detected, attempts to
// parse the markup into structured ToolCalls so the runner can execute them.
func NormalizeAssistantMessage(caps Capabilities, msg Message, _ []ToolSpec) Message {
	msg.Content = strings.TrimSpace(msg.Content)
	if len(msg.ToolCalls) > 0 {
		msg.Content = StripTextualToolCallMarkup(msg.Content)
	} else if caps.RepairTextualToolCalls && LooksLikeTextualToolCall(msg.Content) {
		if parsed := ParseTextualToolCalls(msg.Content); len(parsed) > 0 {
			msg.ToolCalls = parsed
			msg.Content = StripTextualToolCallMarkup(msg.Content)
		}
	}
	return msg
}

// ParseTextualToolCalls attempts to extract structured ToolCall objects from
// textual tool-call markup in content. Returns nil if no parseable calls found.
func ParseTextualToolCalls(content string) []ToolCall {
	var calls []ToolCall

	// Pattern 1: <tool_invocation name="NAME" arguments=ARGS />
	for _, match := range reParseToolInvocationQuoted.FindAllStringSubmatch(content, -1) {
		if call, ok := buildToolCall(match[1], match[2]); ok {
			calls = append(calls, call)
		}
	}
	if len(calls) > 0 {
		return calls
	}
	for _, match := range reParseToolInvocation.FindAllStringSubmatch(content, -1) {
		if call, ok := buildToolCall(match[1], match[2]); ok {
			calls = append(calls, call)
		}
	}
	if len(calls) > 0 {
		return calls
	}

	// Pattern 2: <function=NAME>ARGS</function>
	for _, match := range reParseFunctionTag.FindAllStringSubmatch(content, -1) {
		if call, ok := buildToolCall(match[1], match[2]); ok {
			calls = append(calls, call)
		}
	}
	if len(calls) > 0 {
		return calls
	}

	// Pattern 3: DSML (DeepSeek) format
	for _, match := range reParseDSMLInvoke.FindAllStringSubmatch(content, -1) {
		if call, ok := buildToolCall(match[1], match[2]); ok {
			calls = append(calls, call)
		}
	}
	if len(calls) > 0 {
		return calls
	}

	// Pattern 4: <tool_name>NAME</tool_name><parameters>ARGS</parameters>
	for _, match := range reParseToolNameParams.FindAllStringSubmatch(content, -1) {
		if call, ok := buildToolCall(match[1], match[2]); ok {
			calls = append(calls, call)
		}
	}
	return calls
}

var textualCallCounter int64

func buildToolCall(name, rawArgs string) (ToolCall, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolCall{}, false
	}
	args := normalizeToolArgs(rawArgs)
	textualCallCounter++
	return ToolCall{
		ID:   fmt.Sprintf("textual_%d", textualCallCounter),
		Type: "function",
		Function: ToolFunction{
			Name:      name,
			Arguments: args,
		},
	}, true
}

func normalizeToolArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if json.Valid([]byte(raw)) {
		return raw
	}
	// Some models wrap JSON in extra whitespace or add trailing content.
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start >= 0 && end > start {
		candidate := raw[start : end+1]
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return "{}"
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
	content = reDSMLToolCalls.ReplaceAllString(content, "")
	content = reDSMLInvoke.ReplaceAllString(content, "")
	content = reToolNameBlock.ReplaceAllString(content, "")
	content = reParametersBlock.ReplaceAllString(content, "")
	content = reToolCallCodeBlock.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}
