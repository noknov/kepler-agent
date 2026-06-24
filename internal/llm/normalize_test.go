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
		{`<tool_invocation name="repo-search" arguments={"query": "foo", "repo": "bar"} />`, true},
		{`<tool_invocation name="x" arguments={} /><tool_invocation name="y" arguments={} />`, true},
		{`<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name="code-search"></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls>`, true},
	}
	for _, tc := range cases {
		if got := LooksLikeTextualToolCall(tc.content); got != tc.want {
			t.Fatalf("LooksLikeTextualToolCall(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestStripTextualToolCallMarkup(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"plain text", "plain text"},
		{"<tool_call><function=search></function></tool_call>", ""},
		{"answer\n<tool_call><function=x></function></tool_call>", "answer"},
		{`<tool_invocation name="search" arguments={"q":"x"} />`, ""},
		{`answer <tool_invocation name="x" arguments={} />`, "answer"},
		{`<tool_invocation name="x" arguments={}>body</tool_invocation>`, ""},
		{"<tool_name>search</tool_name><parameters>{}</parameters>", ""},
		{"answer\n<tool_name>x</tool_name>", "answer"},
		{"```json\ntool_call\n```", ""},
		{"answer\n```\ntool_call\n```", "answer"},
		{`answer
<｜｜DSML｜｜tool_calls>
<｜｜DSML｜｜invoke name="code-search">
<｜｜DSML｜｜parameter name="query" string="true">CatalogInfo</｜｜DSML｜｜parameter>
</｜｜DSML｜｜invoke>
</｜｜DSML｜｜tool_calls>`, "answer"},
	}
	for _, tc := range cases {
		got := StripTextualToolCallMarkup(tc.input)
		if got != tc.want {
			t.Errorf("StripTextualToolCallMarkup(%q) = %q, want %q", tc.input, got, tc.want)
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
