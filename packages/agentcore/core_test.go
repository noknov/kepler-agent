package agentcore

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/agentprotocol"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestCoreEmitsVersionedTurnAndMessageLifecycle(t *testing.T) {
	var events []agentprotocol.Event
	core := Core{
		Runner: agent.Runner{LLM: streamingClient{}, Tools: registry.New(), MaxSteps: 1},
		Events: agentprotocol.SinkFunc(func(_ context.Context, event agentprotocol.Event) {
			events = append(events, event)
		}),
	}
	result, err := core.Execute(context.Background(), TurnRequest{
		ThreadID: "thread-1",
		Agent:    agent.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent.Final != "hello" || result.TurnID == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	want := []agentprotocol.EventType{
		agentprotocol.TurnStarted,
		agentprotocol.ItemStarted,
		agentprotocol.ItemDelta,
		agentprotocol.ItemCompleted,
		agentprotocol.TurnCompleted,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for i, event := range events {
		if event.Type != want[i] || event.Version != agentprotocol.Version || event.ID == "" {
			t.Fatalf("event[%d] = %#v", i, event)
		}
	}
}

func TestCoreEmitsMessageLifecycleWithoutStreamDeltas(t *testing.T) {
	var events []agentprotocol.Event
	core := Core{
		Runner: agent.Runner{LLM: nonStreamingClient{}, Tools: registry.New(), MaxSteps: 1},
		Events: agentprotocol.SinkFunc(func(_ context.Context, event agentprotocol.Event) {
			events = append(events, event)
		}),
	}
	if _, err := core.Execute(context.Background(), TurnRequest{
		ThreadID: "thread-1",
		Agent:    agent.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}},
	}); err != nil {
		t.Fatal(err)
	}
	want := []agentprotocol.EventType{
		agentprotocol.TurnStarted,
		agentprotocol.ItemStarted,
		agentprotocol.ItemCompleted,
		agentprotocol.TurnCompleted,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for i, event := range events {
		if event.Type != want[i] {
			t.Fatalf("event[%d].type = %q, want %q", i, event.Type, want[i])
		}
	}
}

type streamingClient struct{}

func (streamingClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hello"}}, nil
}

type nonStreamingClient struct{}

func (nonStreamingClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hello"}}, nil
}

func (nonStreamingClient) ChatStream(context.Context, llm.Request, llm.StreamHandler) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hello"}}, nil
}

func (streamingClient) ChatStream(_ context.Context, _ llm.Request, handler llm.StreamHandler) (llm.Response, error) {
	if handler.OnText != nil {
		handler.OnText("hello")
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hello"}, Streamed: true}, nil
}
