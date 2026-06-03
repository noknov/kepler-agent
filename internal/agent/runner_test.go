package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestLooksRepetitive(t *testing.T) {
	repeated := ""
	for i := 0; i < 20; i++ {
		repeated += "让我查看一下后端中这三个功能的具体实现和保存逻辑。"
	}
	if !looksRepetitive(repeated) {
		t.Fatal("expected repeated Chinese sentence to be detected")
	}
	if looksRepetitive("这里是正常回答。它有多个句子，但没有不断重复同一句话。") {
		t.Fatal("did not expect short normal text to be repetitive")
	}
}

func TestRunnerDropsToolCallPreamble(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{
			Role:    "assistant",
			Content: "我先查看文件。",
			ToolCalls: []llm.ToolCall{{
				ID:   "tool_1",
				Type: "function",
				Function: llm.ToolFunction{
					Name:      "echo",
					Arguments: `{"text":"ok"}`,
				},
			}},
		}},
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 4}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Generated[0].Content != "" {
		t.Fatalf("tool-call preamble was persisted: %q", result.Generated[0].Content)
	}
}

func TestRunnerRejectsRepetitiveFinal(t *testing.T) {
	repeated := ""
	for i := 0; i < 20; i++ {
		repeated += "让我查看一下后端中这三个功能的具体实现和保存逻辑。"
	}
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: repeated}},
		{Message: llm.Message{Role: "assistant", Content: repeated}},
	}}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 2}.Run(context.Background(), Request{})
	if !errors.Is(err, ErrRepetitiveOutput) {
		t.Fatalf("Run() error = %v, want ErrRepetitiveOutput", err)
	}
	if len(result.Generated) != 0 {
		t.Fatalf("repetitive output should not be persisted: %#v", result.Generated)
	}
}

func TestRunnerRejectsTextualToolCallFinal(t *testing.T) {
	textual := "<tool_call>\n<function=code-search>\n<parameter=query>test</parameter>\n</function>\n</tool_call>"
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: textual}},
		{Message: llm.Message{Role: "assistant", Content: textual}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	_, err := Runner{LLM: client, Tools: tools, MaxSteps: 4}.Run(context.Background(), Request{})
	if !errors.Is(err, ErrTextualToolCall) {
		t.Fatalf("Run() error = %v, want ErrTextualToolCall", err)
	}
}

func TestRunnerRetriesTextualToolCallThenSucceeds(t *testing.T) {
	textual := "<tool_call><function=code-search></function></tool_call>"
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: textual}},
		{Message: llm.Message{Role: "assistant", Content: "这是基于已有证据的总结。"}},
	}}

	tools := registry.New()
	tools.Register(fakeTool{})
	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 4}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "这是基于已有证据的总结。" {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(client.requests))
	}
}

func TestRunnerRetriesRepetitiveFinal(t *testing.T) {
	repeated := ""
	for i := 0; i < 20; i++ {
		repeated += "让我查看一下后端中这三个功能的具体实现和保存逻辑。"
	}
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: repeated}},
		{Message: llm.Message{Role: "assistant", Content: "这是修正后的最终回答。"}},
	}}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 3}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "这是修正后的最终回答。" {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(client.requests))
	}
	lastRequest := client.requests[len(client.requests)-1]
	foundRetryPrompt := false
	for _, msg := range lastRequest.Messages {
		if msg.Role == "system" && msg.Content == repetitiveRetryPrompt {
			foundRetryPrompt = true
			break
		}
	}
	if !foundRetryPrompt {
		t.Fatal("retry prompt was not added to the second request")
	}
}

func TestRunnerInjectsBudgetWarningNearMaxSteps(t *testing.T) {
	const maxSteps = 14
	responses := make([]llm.Response, maxSteps)
	for i := range responses {
		responses[i] = llm.Response{Message: toolCallMessage("tool_"+string(rune('a'+i)), fmt.Sprintf(`{"text":"%d"}`, i))}
	}
	client := &fakeClient{responses: responses}
	tools := registry.New()
	tools.Register(fakeTool{})

	_, err := Runner{LLM: client, Tools: tools, MaxSteps: maxSteps}.Run(context.Background(), Request{})
	if !errors.Is(err, ErrMaxToolSteps) {
		t.Fatalf("Run() error = %v, want ErrMaxToolSteps", err)
	}
	// Warning should be injected at step maxSteps-10 = step 4
	warningStep := maxSteps - 10
	want := budgetWarningPrompt(9)
	found := false
	for i := warningStep; i < len(client.requests); i++ {
		for _, msg := range client.requests[i].Messages {
			if msg.Role == "system" && msg.Content == want {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("budget warning not found from step %d onward", warningStep)
	}
}

func TestRunnerReturnsMaxToolStepsError(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: toolCallMessage("tool_1", `{"text":"1"}`)},
		{Message: toolCallMessage("tool_2", `{"text":"2"}`)},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	_, err := Runner{LLM: client, Tools: tools, MaxSteps: 2}.Run(context.Background(), Request{})
	if !errors.Is(err, ErrMaxToolSteps) {
		t.Fatalf("Run() error = %v, want ErrMaxToolSteps", err)
	}
}

func toolCallMessage(id, args string) llm.Message {
	return llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:   id,
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "echo",
				Arguments: args,
			},
		}},
	}
}

func TestCompressContextClearsOldToolResults(t *testing.T) {
	r := Runner{MaxContextChars: 500}
	messages := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "question"},
		{Role: "assistant"},
		{Role: "tool", ToolCallID: "1", Content: strings.Repeat("x", 200)},
		{Role: "assistant"},
		{Role: "tool", ToolCallID: "2", Content: strings.Repeat("y", 200)},
		// recent messages (within last 8)
		{Role: "assistant"},
		{Role: "tool", ToolCallID: "3", Content: strings.Repeat("z", 200)},
		{Role: "assistant"},
		{Role: "tool", ToolCallID: "4", Content: "recent"},
		{Role: "assistant", Content: "thinking..."},
		{Role: "tool", ToolCallID: "5", Content: "latest"},
	}
	result := r.compressContext(messages)
	// Old tool results (before boundary) should be cleared
	if result[3].Content != toolResultCleared {
		t.Fatalf("expected old tool result [1] to be cleared, got len=%d", len(result[3].Content))
	}
	// Recent results (within last 8) should be preserved
	if result[11].Content != "latest" {
		t.Fatalf("expected recent tool result to be preserved, got: %s", result[11].Content)
	}
}

func TestCompressContextNoOpUnderLimit(t *testing.T) {
	r := Runner{MaxContextChars: 10000}
	messages := []llm.Message{
		{Role: "system", Content: "prompt"},
		{Role: "tool", ToolCallID: "1", Content: "short"},
	}
	result := r.compressContext(messages)
	if result[1].Content != "short" {
		t.Fatal("should not compress when under limit")
	}
}

type fakeClient struct {
	responses []llm.Response
	requests  []llm.Request
}

func (f *fakeClient) Chat(_ llm.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("unexpected chat call")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

type fakeTool struct{}

func (fakeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "echo",
			Description: "echo input",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: string(args)}, nil
}
