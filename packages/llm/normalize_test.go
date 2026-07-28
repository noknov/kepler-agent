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
		{`<tool_invocation name="repo-search" arguments='{"query":"foo"}' />`, true},
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
		Role:    "assistant",
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

func TestParseTextualToolCalls(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantCount int
		wantName  string
		wantArgs  string
	}{
		{
			name:      "tool_invocation self-closing",
			content:   `<tool_invocation name="repo-search" arguments={"query": "foo", "repo": "bar"} />`,
			wantCount: 1,
			wantName:  "repo-search",
			wantArgs:  `{"query": "foo", "repo": "bar"}`,
		},
		{
			name:      "quoted tool_invocation args",
			content:   `<tool_invocation name="repo-search" arguments='{"query":"foo"}' />`,
			wantCount: 1,
			wantName:  "repo-search",
			wantArgs:  `{"query":"foo"}`,
		},
		{
			name:      "tool_invocation with body",
			content:   `<tool_invocation name="code-search" arguments={"query":"test"}>body</tool_invocation>`,
			wantCount: 1,
			wantName:  "code-search",
			wantArgs:  `{"query":"test"}`,
		},
		{
			name:      "multiple tool_invocations",
			content:   `<tool_invocation name="x" arguments={"a":1} /><tool_invocation name="y" arguments={"b":2} />`,
			wantCount: 2,
			wantName:  "x",
		},
		{
			name:      "function tag",
			content:   `<tool_call><function=code-search>{"query":"hello"}</function></tool_call>`,
			wantCount: 1,
			wantName:  "code-search",
			wantArgs:  `{"query":"hello"}`,
		},
		{
			name:      "no match",
			content:   "plain text answer",
			wantCount: 0,
		},
		{
			name:      "empty args",
			content:   `<tool_invocation name="demo-tool" arguments={} />`,
			wantCount: 1,
			wantName:  "demo-tool",
			wantArgs:  "{}",
		},
		{
			name:      "tool_name parameters",
			content:   `<tool_name>code-search</tool_name><parameters>{"query":"handler"}</parameters>`,
			wantCount: 1,
			wantName:  "code-search",
			wantArgs:  `{"query":"handler"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := ParseTextualToolCalls(tc.content)
			if len(calls) != tc.wantCount {
				t.Fatalf("got %d calls, want %d: %#v", len(calls), tc.wantCount, calls)
			}
			if tc.wantCount > 0 && calls[0].Function.Name != tc.wantName {
				t.Fatalf("name = %q, want %q", calls[0].Function.Name, tc.wantName)
			}
			if tc.wantArgs != "" && calls[0].Function.Arguments != tc.wantArgs {
				t.Fatalf("args = %q, want %q", calls[0].Function.Arguments, tc.wantArgs)
			}
		})
	}
}

func TestMayBecomeTextualToolCall(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{"normal prose", false},
		{"normal prose <", true},
		{"normal prose <tool_", true},
		{"normal prose <function", true},
		{"normal prose ```", true},
	}
	for _, tc := range cases {
		if got := MayBecomeTextualToolCall(tc.content); got != tc.want {
			t.Fatalf("MayBecomeTextualToolCall(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestNormalizeAssistantMessageParsesTextualCalls(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: `I'll search for that. <tool_invocation name="code-search" arguments={"query":"handler"} />`,
	}
	// OpenAI-compatible protocol enables textual tool call repair.
	caps := CapabilitiesFor("deepseek", "openai")
	out := NormalizeAssistantMessage(caps, msg, nil)
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected 1 parsed tool call, got %d", len(out.ToolCalls))
	}
	if out.ToolCalls[0].Function.Name != "code-search" {
		t.Fatalf("name = %q, want code-search", out.ToolCalls[0].Function.Name)
	}
	if LooksLikeTextualToolCall(out.Content) {
		t.Fatalf("content still has markup: %q", out.Content)
	}
}

func TestNormalizeAssistantMessageRepairsForAllProtocols(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: `Some text with <tool_invocation name="code-search" arguments={"query":"x"} />`,
	}
	// Even Anthropic-compatible providers (MiMo) may leak markup into text blocks.
	caps := CapabilitiesFor("mimo", "anthropic")
	out := NormalizeAssistantMessage(caps, msg, nil)
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected repair to parse 1 tool call, got %d", len(out.ToolCalls))
	}
}
