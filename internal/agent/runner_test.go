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

func TestRunnerStripsTextualToolCallMarkupOnRetry(t *testing.T) {
	textual := "Here is the answer.\n<tool_call>\n<function=code-search>\n<parameter=query>test</parameter>\n</function>\n</tool_call>"
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: textual}},
		{Message: llm.Message{Role: "assistant", Content: textual}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 4}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "Here is the answer." {
		t.Fatalf("Final = %q, want stripped text", result.Final)
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
	// Retry should still include tools so the model can make structured calls.
	retryReq := client.requests[1]
	if len(retryReq.Tools) == 0 {
		t.Fatal("retry request should still include tools")
	}
}

func TestRunnerRetriesTextualToolCallThenUsesStructuredCalls(t *testing.T) {
	textual := "<tool_call><function=echo><parameter=text>hello</parameter></function></tool_call>"
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: textual}},
		{Message: toolCallMessage("tool_retry", `{"text":"structured"}`)},
		{Message: llm.Message{Role: "assistant", Content: "最终结果。"}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "最终结果。" {
		t.Fatalf("Final = %q, want 最终结果。", result.Final)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(client.requests))
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
		if msg.Role == "system" && msg.Content == repetitiveRetryPrompt() {
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

func TestRunnerSkipsDuplicateToolCallInsteadOfFailing(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: toolCallMessage("tool_1", `{"text":"same"}`)},
		{Message: toolCallMessage("tool_2", `{"text":"same"}`)},
		{Message: toolCallMessage("tool_3", `{"text":"same"}`)},
		{Message: llm.Message{Role: "assistant", Content: "final"}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "final" {
		t.Fatalf("Final = %q, want final", result.Final)
	}
	foundDuplicate := false
	for _, msg := range result.Generated {
		if msg.Role == "tool" && strings.Contains(msg.Content, "duplicate echo call skipped") {
			foundDuplicate = true
			break
		}
	}
	if !foundDuplicate {
		t.Fatalf("duplicate tool result not found: %#v", result.Generated)
	}
}

func TestRunnerInjectsSteeringBeforeNextStep(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: toolCallMessage("tool_1", `{"text":"initial"}`)},
		{Message: llm.Message{Role: "assistant", Content: "final"}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})
	steeringCalls := 0

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 4}.Run(context.Background(), Request{
		Steering: func() []llm.Message {
			steeringCalls++
			if steeringCalls != 2 {
				return nil
			}
			return []llm.Message{{Role: "user", Content: "switch to staging"}}
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "final" {
		t.Fatalf("Final = %q, want final", result.Final)
	}
	if len(client.requests) < 2 {
		t.Fatalf("expected at least 2 LLM requests, got %d", len(client.requests))
	}
	found := false
	for _, msg := range client.requests[1].Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "switch to staging") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("steering message not found in second request: %#v", client.requests[1].Messages)
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
	errors    []error
	requests  []llm.Request
}

func (f *fakeClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return llm.Response{}, err
		}
	}
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("unexpected chat call")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func TestRunnerRetriesEmptyResponseThenSucceeds(t *testing.T) {
	client := &fakeClient{
		errors: []error{
			llm.EmptyResponseError{Provider: "anthropic messages", StopReason: "end_turn"},
			nil,
		},
		responses: []llm.Response{
			{Message: llm.Message{Role: "assistant", Content: "这是重试后的回答。"}},
		},
	}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 3}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "这是重试后的回答。" {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(client.requests))
	}
	foundRetryPrompt := false
	for _, msg := range client.requests[1].Messages {
		if msg.Role == "system" && msg.Content == emptyResponseRetryPrompt() {
			foundRetryPrompt = true
			break
		}
	}
	if !foundRetryPrompt {
		t.Fatal("empty-response retry prompt was not added to the second request")
	}
}

func TestRepeatableToolAllowsDuplicateCalls(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: repeatableToolCallMessage("tool_1", `{"x":"1"}`)},
		{Message: repeatableToolCallMessage("tool_2", `{"x":"1"}`)},
		{Message: repeatableToolCallMessage("tool_3", `{"x":"1"}`)},
		{Message: llm.Message{Role: "assistant", Content: "final"}},
	}}
	tools := registry.New()
	tools.Register(fakeRepeatableTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "final" {
		t.Fatalf("Final = %q, want final", result.Final)
	}
	for _, msg := range result.Generated {
		if msg.Role == "tool" && strings.Contains(msg.Content, "duplicate") {
			t.Fatal("repeatable tool should never be skipped as duplicate")
		}
	}
}

func repeatableToolCallMessage(id, args string) llm.Message {
	return llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:   id,
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "fetch",
				Arguments: args,
			},
		}},
	}
}

func TestParallelToolExecution(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "t1", Type: "function", Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"a"}`}},
				{ID: "t2", Type: "function", Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"b"}`}},
				{ID: "t3", Type: "function", Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"c"}`}},
			},
		}},
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 4}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	toolResults := 0
	for _, msg := range result.Generated {
		if msg.Role == "tool" {
			toolResults++
		}
	}
	if toolResults != 3 {
		t.Fatalf("expected 3 tool results, got %d", toolResults)
	}
	// Verify order is preserved
	idx := 0
	for _, msg := range result.Generated {
		if msg.Role != "tool" {
			continue
		}
		wantIDs := []string{"t1", "t2", "t3"}
		if msg.ToolCallID != wantIDs[idx] {
			t.Fatalf("tool result %d: ToolCallID = %q, want %q", idx, msg.ToolCallID, wantIDs[idx])
		}
		idx++
	}
}

func TestWaitForUserToolResultIsNotEvidenceWrapped(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   "ask_1",
				Type: "function",
				Function: llm.ToolFunction{
					Name:      "ask",
					Arguments: `{"question":"which repo?"}`,
				},
			}},
		}},
	}}
	tools := registry.New()
	tools.Register(fakeWaitTool{})

	result, err := Runner{
		LLM:    client,
		Tools:  tools,
		Format: fakeObservationFormatter{},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Pending {
		t.Fatal("expected pending result")
	}
	if result.PendingQuestion != "which repo?" {
		t.Fatalf("PendingQuestion = %q, want raw question", result.PendingQuestion)
	}
	for _, msg := range result.Generated {
		if msg.Role == "tool" && strings.Contains(msg.Content, "<evidence") {
			t.Fatalf("wait-for-user tool result should not be evidence wrapped: %q", msg.Content)
		}
	}
}

func TestHasFencedCodeBlock(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"no code here", false},
		{"use `inline` code", false},
		{"```\nsome code\n```", true},
		{"```go\nfunc f() {}\n```", true},
		{"text before\n```csharp\nif (x) {}\n```\ntext after", true},
		{"   ```\nindented fence\n```", true},
	}
	for _, c := range cases {
		if got := hasFencedCodeBlock(c.text); got != c.want {
			t.Errorf("hasFencedCodeBlock(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestRunnerRetriesOnCodeClaimWithoutCodeTool(t *testing.T) {
	codeAnswer := "Here is the code:\n```csharp\nif (x.test) { return; }\n```"
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: codeAnswer}},
		{Message: llm.Message{Role: "assistant", Content: "I cannot verify this without reading the file."}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "I cannot verify this without reading the file." {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(client.requests))
	}
	retryPrompt := codeClaimRetryPrompt()
	found := false
	for _, msg := range client.requests[1].Messages {
		if msg.Role == "system" && msg.Content == retryPrompt {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("code_claim_retry prompt was not injected")
	}
}

func TestRunnerNoCodeClaimRetryWhenCodeToolCalled(t *testing.T) {
	codeAnswer := "Here is the code:\n```csharp\nif (x.test) { return; }\n```"
	client := &fakeClient{responses: []llm.Response{
		{Message: codeToolCallMessage("read_1", `{"path":"ShopifyService.cs"}`)},
		{Message: llm.Message{Role: "assistant", Content: codeAnswer}},
	}}
	tools := registry.New()
	tools.Register(fakeCodeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != codeAnswer {
		t.Fatalf("Final = %q", result.Final)
	}
	// Only 2 LLM calls: tool step + final answer; no retry injected.
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 LLM calls (no retry), got %d", len(client.requests))
	}
}

func TestRunnerNoCodeClaimRetryWhenNoCodeBlock(t *testing.T) {
	answer := "The field `test` is just a boolean on the model."
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: answer}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 3}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != answer {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(client.requests))
	}
}

func TestRunnerCodeClaimRetryOnlyOnce(t *testing.T) {
	codeAnswer := "```csharp\nif (x.test) { return; }\n```"
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: codeAnswer}},
		{Message: llm.Message{Role: "assistant", Content: codeAnswer}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Second response still has code block but retry already used; accepted as-is.
	if result.Final != codeAnswer {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected exactly 2 LLM calls (one retry), got %d", len(client.requests))
	}
}

func codeToolCallMessage(id, args string) llm.Message {
	return llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:   id,
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "git-read_file_ref",
				Arguments: args,
			},
		}},
	}
}

// fakeCodeTool simulates a code-reading tool (git-read_file_ref) to verify
// that the code claim retry is suppressed when evidence was gathered.
type fakeCodeTool struct{}

func (fakeCodeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "git-read_file_ref",
			Description: "read file at git ref",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeCodeTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: string(args)}, nil
}

type fakeTool struct{}

func (fakeTool) Parallel() bool { return true }

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

type fakeRepeatableTool struct{}

func (fakeRepeatableTool) Repeatable() bool { return true }

func (fakeRepeatableTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "fetch",
			Description: "fetch data",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeRepeatableTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: string(args)}, nil
}

type fakeWaitTool struct{}

func (fakeWaitTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "ask",
			Description: "ask user",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeWaitTool) Execute(_ context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: args.Question, WaitForUser: true}, nil
}

type fakeObservationFormatter struct{}

func (fakeObservationFormatter) ToolObservation(toolName string, output string) string {
	return "<evidence source=\"" + toolName + "\">\n" + output + "\n</evidence>"
}
