package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/agentcore"
	"github.com/noknov/slack-copilot-agent/packages/agentprotocol"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestInitializeAdvertisesProtocolVersion(t *testing.T) {
	server := New(&agentcore.Core{})
	var output bytes.Buffer
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.ProtocolVersion != agentprotocol.Version {
		t.Fatalf("protocol version = %q", response.Result.ProtocolVersion)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestTurnStartStreamsSequencedLifecycle(t *testing.T) {
	core := &agentcore.Core{Runner: agent.Runner{LLM: appServerStreamClient{}, Tools: registry.New(), MaxSteps: 1}}
	server := New(core)
	var output lockedBuffer
	server.writer = &output
	server.handle(context.Background(), Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "turn/start",
		Params:  json.RawMessage(`{"threadId":"thread-1","messages":[{"role":"user","content":"hello"}]}`),
	})

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output.String(), `"type":"turn.completed"`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 7 {
		t.Fatalf("expected response and lifecycle events, got:\n%s", output.String())
	}
	var lastSequence uint64
	for _, line := range lines[1:] {
		var notification struct {
			Method string              `json:"method"`
			Params agentprotocol.Event `json:"params"`
		}
		if json.Unmarshal([]byte(line), &notification) != nil || notification.Method != "event" {
			continue
		}
		if notification.Params.Sequence <= lastSequence {
			t.Fatalf("sequence did not increase: %d after %d", notification.Params.Sequence, lastSequence)
		}
		lastSequence = notification.Params.Sequence
	}
}

type appServerStreamClient struct{}

func (appServerStreamClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hello"}}, nil
}
func (appServerStreamClient) ChatStream(_ context.Context, _ llm.Request, handler llm.StreamHandler) (llm.Response, error) {
	if handler.OnText != nil {
		handler.OnText("hello")
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "hello"}, Streamed: true}, nil
}
