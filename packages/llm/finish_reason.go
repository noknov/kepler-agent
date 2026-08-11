package llm

import "strings"

// completedFinishReason supplies the OpenAI-compatible default only after a
// successful response or an explicit stream completion marker. Some compatible
// providers omit finish_reason despite returning a complete response.
func completedFinishReason(reason string, message Message) string {
	if strings.TrimSpace(reason) != "" {
		return reason
	}
	if len(message.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}
