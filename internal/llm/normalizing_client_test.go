package llm

import (
	"context"
	"testing"
)

type stubClient struct {
	resp Response
}

func (s *stubClient) Chat(_ context.Context, _ Request) (Response, error) {
	return s.resp, nil
}

func TestNormalizingClientStripsMarkupWhenToolCallsPresent(t *testing.T) {
	inner := &stubClient{resp: Response{Message: Message{
		Role:    "assistant",
		Content: "note\n<tool_call><function=x></function></tool_call>",
		ToolCalls: []ToolCall{{
			ID: "1", Type: "function",
			Function: ToolFunction{Name: "echo", Arguments: `{}`},
		}},
	}}}
	client := WrapClient(inner, CapabilitiesFor("mimo", "anthropic"))
	resp, err := client.Chat(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if LooksLikeTextualToolCall(resp.Message.Content) {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}
