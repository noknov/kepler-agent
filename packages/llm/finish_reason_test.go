package llm

import "testing"

func TestCompletedFinishReason(t *testing.T) {
	tests := []struct {
		name    string
		reason  string
		message Message
		want    string
	}{
		{name: "preserves provider reason", reason: "length", want: "length"},
		{name: "text response defaults to stop", message: Message{Content: "done"}, want: "stop"},
		{name: "tool response defaults to tool calls", message: Message{ToolCalls: []ToolCall{{ID: "call_1"}}}, want: "tool_calls"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := completedFinishReason(test.reason, test.message); got != test.want {
				t.Fatalf("completedFinishReason(%q, %#v) = %q, want %q", test.reason, test.message, got, test.want)
			}
		})
	}
}
