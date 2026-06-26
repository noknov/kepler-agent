package memory

import (
	"testing"

	"github.com/wati/oncall-agent/internal/llm"
)

func TestPrepareForLLMFillsMissingToolResponses(t *testing.T) {
	messages := []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_a", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{}`}},
			{ID: "call_b", Type: "function", Function: llm.ToolFunction{Name: "explore-code", Arguments: `{}`}},
		}},
		{Role: "tool", ToolCallID: "call_a", Name: "code-search", Content: "hit"},
	}

	out := PrepareForLLM(messages)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[2].ToolCallID != "call_b" {
		t.Fatalf("missing tool response for call_b: %#v", out[2])
	}
}

func TestPrepareForLLMKeepsReasoningAndStripsEmptyToolCalls(t *testing.T) {
	messages := []llm.Message{
		{Role: "assistant", ReasoningContent: "thinking...", ToolCalls: []llm.ToolCall{
			{Type: "function", Function: llm.ToolFunction{Name: "", Arguments: `{}`}},
			{ID: "call_a", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{}`}},
		}},
		{Role: "tool", ToolCallID: "call_a", Name: "code-search", Content: "hit"},
	}

	out := PrepareForLLM(messages)
	if out[0].ReasoningContent != "thinking..." {
		t.Fatal("reasoning content should be preserved for providers that require it")
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("empty tool calls should be removed: %#v", out[0].ToolCalls)
	}
}

func TestPrepareForLLMMovesInterruptedToolResultNextToAssistant(t *testing.T) {
	messages := []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_a", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{}`}},
		}},
		{Role: "system", Content: "pivot"},
		{Role: "tool", ToolCallID: "call_a", Name: "code-search", Content: "late hit"},
	}

	out := PrepareForLLM(messages)
	if out[1].Role != "tool" || out[1].ToolCallID != "call_a" {
		t.Fatalf("expected immediate tool response after assistant, got %#v", out[1])
	}
	if out[1].Content != "late hit" {
		t.Fatalf("expected interrupted tool result to be moved, got %q", out[1].Content)
	}
	if out[2].Role != "system" {
		t.Fatalf("expected system pivot after closed tool group, got %#v", out[2])
	}
}

func TestPrepareForLLMDropsOrphanToolResults(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "pivot"},
		{Role: "tool", ToolCallID: "orphan", Name: "code-search", Content: "late hit"},
		{Role: "user", Content: "next"},
	}

	out := PrepareForLLM(messages)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(out), out)
	}
	for _, msg := range out {
		if msg.Role == "tool" {
			t.Fatalf("orphan tool result was retained: %#v", out)
		}
	}
}
