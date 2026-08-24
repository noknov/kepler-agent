package llm

import (
	"context"
	"testing"
)

type stubWireClient struct {
	name      string
	lastModel string
}

func (s *stubWireClient) Chat(_ context.Context, req Request) (Response, error) {
	s.lastModel = req.Model
	return Response{Message: Message{Role: "assistant", Content: s.name}}, nil
}

func (s *stubWireClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	s.lastModel = req.Model
	if h.OnText != nil {
		h.OnText(s.name)
	}
	return s.Chat(ctx, req)
}

func TestProtocolRouterUsesResponsesForConfiguredModels(t *testing.T) {
	openAI := &stubWireClient{name: "openai"}
	responses := &stubWireClient{name: "responses"}
	router := NewProtocolRouter(openAI, responses, []string{"gpt-5.6-luna"})

	resp, err := router.Chat(context.Background(), Request{Model: "ox-alpha-free", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Message.Content != "openai" {
		t.Fatalf("primary Chat() content = %q, want openai", resp.Message.Content)
	}

	resp, err = router.Chat(context.Background(), Request{Model: "gpt-5.6-luna", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("responses Chat() error = %v", err)
	}
	if resp.Message.Content != "responses" {
		t.Fatalf("responses Chat() content = %q, want responses", resp.Message.Content)
	}
}

func TestProtocolRouterChatStreamRoutesByModel(t *testing.T) {
	openAI := &stubWireClient{name: "openai"}
	responses := &stubWireClient{name: "responses"}
	router := NewProtocolRouter(openAI, responses, []string{"gpt-5.6-luna"})

	_, err := router.ChatStream(context.Background(), Request{Model: "gpt-5.6-luna", Messages: []Message{{Role: "user", Content: "hi"}}}, StreamHandler{})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if responses.lastModel != "gpt-5.6-luna" {
		t.Fatalf("responses model = %q, want gpt-5.6-luna", responses.lastModel)
	}
}
