package llm

import "testing"

func TestLooksLikeTextualToolCall(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{"", false},
		{"Here is the architecture summary.", false},
		{"<tool_call>\n<function=code-search>\n</function>\n</tool_call>", true},
		{"I'll call <function=git-log> next.", true},
		{"normal markdown with `code` only", false},
	}
	for _, tc := range cases {
		if got := LooksLikeTextualToolCall(tc.content); got != tc.want {
			t.Fatalf("LooksLikeTextualToolCall(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestNormalizeAssistantMessageStripsMarkupWhenStructuredCallsPresent(t *testing.T) {
	msg := Message{
		Role: "assistant",
		Content: "Planning...\n<tool_call><function=code-search></function></tool_call>",
		ToolCalls: []ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: ToolFunction{
				Name:      "code-search",
				Arguments: `{"query":"x"}`,
			},
		}},
	}
	out := NormalizeAssistantMessage(CapabilitiesFor("mimo", "anthropic"), msg, nil)
	if LooksLikeTextualToolCall(out.Content) {
		t.Fatalf("content still looks like textual tool call: %q", out.Content)
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("tool calls were dropped: %#v", out.ToolCalls)
	}
}
