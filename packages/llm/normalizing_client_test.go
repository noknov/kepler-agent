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

func TestNormalizingClientDoesNotFakeStreamWhenInnerCannotStream(t *testing.T) {
	inner := &stubClient{resp: Response{Message: Message{Role: "assistant", Content: "complete"}}}
	client := WrapClient(inner, CapabilitiesFor("openai", "openai")).(StreamClient)

	called := false
	resp, err := client.ChatStream(context.Background(), Request{}, TextStream(func(string) { called = true }))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("callback should not be called for non-streaming inner client")
	}
	if resp.Streamed {
		t.Fatal("response should not be marked streamed")
	}
	if resp.Message.Content != "complete" {
		t.Fatalf("content = %q, want complete", resp.Message.Content)
	}
}
