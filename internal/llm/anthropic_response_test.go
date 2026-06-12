package llm

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAnthropicChatParsesToolUseBlocks(t *testing.T) {
	raw := anthropicResponse{
		Content: []anthropicBlock{
			{Type: "text", Text: "checking"},
			{
				Type:  "tool_use",
				ID:    "toolu_abc",
				Name:  "code-search",
				Input: json.RawMessage(`{"query":"Startup"}`),
			},
		},
		StopReason: "tool_use",
	}
	message := Message{Role: "assistant", Content: raw.text()}
	for _, block := range raw.Content {
		if block.Type != "tool_use" {
			continue
		}
		args := "{}"
		if len(block.Input) > 0 {
			args = string(block.Input)
		}
		message.ToolCalls = append(message.ToolCalls, ToolCall{
			ID:   block.ID,
			Type: "function",
			Function: ToolFunction{
				Name:      block.Name,
				Arguments: args,
			},
		})
	}
	message = NormalizeAssistantMessage(CapabilitiesFor("mimo", "anthropic"), message, nil)

	if len(message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v", message.ToolCalls)
	}
	if message.ToolCalls[0].Function.Name != "code-search" {
		t.Fatalf("tool name = %q", message.ToolCalls[0].Function.Name)
	}
	if message.Content != "checking" {
		t.Fatalf("content = %q", message.Content)
	}
}

func TestAnthropicEmptyResponseErrorMetadata(t *testing.T) {
	raw := anthropicResponse{
		Content: []anthropicBlock{
			{Type: "thinking", Text: "hidden reasoning"},
		},
		StopReason: "end_turn",
	}
	err := EmptyResponseError{
		Provider:     "anthropic messages",
		StopReason:   raw.StopReason,
		ContentTypes: raw.contentTypes(),
	}

	if !IsEmptyResponse(err) {
		t.Fatal("expected EmptyResponseError to be recognized")
	}
	var emptyErr EmptyResponseError
	if !errors.As(err, &emptyErr) {
		t.Fatal("expected errors.As to find EmptyResponseError")
	}
	if got := emptyErr.ContentTypes; len(got) != 1 || got[0] != "thinking" {
		t.Fatalf("ContentTypes = %#v, want [thinking]", got)
	}
	if emptyErr.Error() != "anthropic messages returned no text or tool calls (stop_reason=end_turn)" {
		t.Fatalf("Error() = %q", emptyErr.Error())
	}
}
