package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/memory"
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

func TestRunnerRetriesTemporaryProviderError(t *testing.T) {
	client := &fakeClient{
		errors: []error{
			llm.ProviderError{Provider: "opencode-go stream", StatusCode: 522, Body: "error code: 522"},
		},
		responses: []llm.Response{
			{Message: llm.Message{Role: "assistant", Content: "done after retry"}},
		},
	}

	var statuses []string
	result, err := Runner{
		LLM:          client,
		Tools:        registry.New(),
		MaxSteps:     4,
		StatusUpdate: func(status string) { statuses = append(statuses, status) },
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "done after retry" {
		t.Fatalf("Final = %q, want retry response", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("Chat calls = %d, want 2", len(client.requests))
	}
	if len(statuses) < 2 {
		t.Fatalf("statuses = %#v, want retry status", statuses)
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

func TestRunnerExecutesRepeatedToolCalls(t *testing.T) {
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
	toolMessages := 0
	for _, msg := range result.Generated {
		if msg.Role == "tool" {
			toolMessages++
			if strings.Contains(msg.Content, "duplicate") {
				t.Fatalf("runner should not synthesize duplicate tool errors: %q", msg.Content)
			}
		}
	}
	if toolMessages != 3 {
		t.Fatalf("tool messages = %d, want 3", toolMessages)
	}
}

func TestRunnerAllowsSameSearchQueryAcrossDifferentScopes(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: codeSearchToolCallMessage("search_1", `{"query":"login","path":"api"}`)},
		{Message: codeSearchToolCallMessage("search_2", `{"query":"login","path":"worker"}`)},
		{Message: codeSearchToolCallMessage("search_3", `{"query":"login","path":"frontend"}`)},
		{Message: llm.Message{Role: "assistant", Content: "final"}},
	}}
	tools := registry.New()
	tools.Register(fakeCodeSearchTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "final" {
		t.Fatalf("Final = %q, want final", result.Final)
	}
	var duplicateMessages []string
	for _, msg := range result.Generated {
		if msg.Role == "tool" && strings.Contains(msg.Content, "duplicate") {
			duplicateMessages = append(duplicateMessages, msg.Content)
		}
	}
	if len(duplicateMessages) > 0 {
		t.Fatalf("same query across different search scopes should not be deduplicated: %#v", duplicateMessages)
	}
}

func TestRunnerDoesNotInterruptOnMissingWorkspacePath(t *testing.T) {
	// Missing file/path errors should NOT trigger clarification — the model
	// should self-correct by trying different paths or repos.
	client := &fakeClient{responses: []llm.Response{
		{Message: repoMissToolCallMessage("tool_1", `{"path":"frontend"}`)},
		{Message: repoMissToolCallMessage("tool_2", `{"path":"wati-frontend"}`)},
		{Message: llm.Message{Role: "assistant", Content: "found it another way"}},
	}}
	tools := registry.New()
	tools.Register(fakeMissingWorkspaceTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 6}.Run(context.Background(), Request{
		Locale: "zh",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Pending {
		t.Fatalf("should not interrupt for missing paths; got PendingQuestion = %q", result.PendingQuestion)
	}
	if len(client.requests) != 3 {
		t.Fatalf("Chat calls = %d, want model to continue all 3 turns", len(client.requests))
	}
}

func TestRunnerDoesNotAskForClarificationWhenCodeSearchMakesProgress(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: repoMissToolCallMessage("tool_1", `{"path":"wati-frontend-app/domains/connectors/src/Integration/_constants/IntegrationCards.tsx"}`)},
		{Message: codeSearchToolCallMessage("tool_2", `{"query":"IntegrationCards","path":"domains/connectors"}`)},
		{Message: repoMissToolCallMessage("tool_3", `{"path":"wati-frontend-app/domains/connectors/src/Integration/Shopify/Catalog/index.tsx"}`)},
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	tools := registry.New()
	tools.Register(fakeMissingWorkspaceTool{})
	tools.Register(fakeCodeSearchTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 8}.Run(context.Background(), Request{
		Locale: "zh",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Pending {
		t.Fatalf("did not expect pending clarification after successful search, got %q", result.PendingQuestion)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	if len(client.requests) != 4 {
		t.Fatalf("Chat calls = %d, want continue through final", len(client.requests))
	}
}

func TestRunnerKeepsToolsAfterManySearchTurns(t *testing.T) {
	client := &longSearchFakeClient{}
	tools := registry.New()
	tools.Register(fakeCodeSearchTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 16}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "final after 12 searches" {
		t.Fatalf("Final = %q, want final after 12 searches", result.Final)
	}
	if len(client.requests) < 12 {
		t.Fatalf("Chat calls = %d, want at least 12", len(client.requests))
	}
	for i := 0; i < 11; i++ {
		if len(client.requests[i].Tools) == 0 {
			t.Fatalf("request %d should still expose tools before final answer", i)
		}
	}
}

func TestRunnerParallelCodeSearchCountsAsOneTurn(t *testing.T) {
	client := &parallelBurstFakeClient{}
	tools := registry.New()
	tools.Register(fakeCodeSearchTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 6}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "done after parallel burst" {
		t.Fatalf("Final = %q, want done after parallel burst", result.Final)
	}
	if len(client.requests) < 2 {
		t.Fatalf("Chat calls = %d, want at least 2", len(client.requests))
	}
	if len(client.requests[1].Tools) == 0 {
		t.Fatal("second request should still expose tools after one parallel search turn")
	}
}

func TestRunnerRetriesRawEvidenceFinal(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "基于证据：\n<evidence source=\"code-search\">\nsecret raw result\n</evidence>"}},
		{Message: llm.Message{Role: "assistant", Content: "Instagram 连接走 Facebook Login for Business，入口是 useConnectFbForInstagram。"}},
	}}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 3}.Run(context.Background(), Request{Locale: "zh"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result.Final, "Facebook Login") {
		t.Fatalf("Final = %q, want synthesized partial answer", result.Final)
	}
	if strings.Contains(result.Final, "<evidence source=") || strings.Contains(result.Final, "调查已达工具上限，暂无法继续搜索") {
		t.Fatalf("Final leaked raw evidence or legacy fallback: %q", result.Final)
	}
	foundRetry := false
	for _, msg := range client.requests[1].Messages {
		if msg.Role == "system" && msg.Content == rawEvidenceRetryPrompt() {
			foundRetry = true
			break
		}
	}
	if !foundRetry {
		t.Fatal("raw evidence retry prompt was not injected")
	}
}

func TestStreamingToolExecutorDoesNotDoubleExecuteInFlightTool(t *testing.T) {
	var calls int32
	tools := registry.New()
	tools.Register(countingSlowTool{calls: &calls})
	call := llm.ToolCall{
		ID:   "slow_1",
		Type: "function",
		Function: llm.ToolFunction{
			Name:      "slow",
			Arguments: `{}`,
		},
	}
	exec := newStreamingToolExecutor(context.Background(), Runner{Tools: tools}, Request{})

	exec.Submit(call)
	results := exec.Drain([]llm.ToolCall{call})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results = %#v", results)
	}
}

func TestStripRawEvidenceDump(t *testing.T) {
	raw := "Summary\n- [code-search] <evidence source=\"code-search\">\npath/to/file.ts:10\n</evidence>\nConclusion"
	got := stripRawEvidenceDump(raw)
	if got != "Summary\nConclusion" {
		t.Fatalf("stripRawEvidenceDump() = %q", got)
	}
}

func TestRunnerCompactMessagesUsesCompactIfNeeded(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "compact summary"}},
	}}
	c := &memory.Compactor{
		MaxContextTokens:    1,
		AutocompactBuffer:   0,
		OutputReserve:       0,
		KeepRecentTools:     1,
		MaxToolResultTokens: 1,
		ClearableTools:      map[string]bool{"code-search": true},
		LLMClient:           client,
		CompactModel:        "compact-model",
	}
	r := Runner{Compactor: c}
	messages := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "assistant"},
		{Role: "tool", Name: "code-search", ToolCallID: "old", Content: strings.Repeat("old ", 100)},
		{Role: "assistant"},
		{Role: "tool", Name: "code-search", ToolCallID: "new", Content: strings.Repeat("new ", 100)},
	}

	compacted := r.compactMessages(context.Background(), messages)
	foundSummary := false
	for _, msg := range compacted {
		if strings.Contains(msg.Content, "compact summary") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatalf("compact summary not found: %#v", compacted)
	}
}

type longSearchFakeClient struct {
	requests []llm.Request
	searches int
}

func (f *longSearchFakeClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	if f.searches >= 12 {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "final after 12 searches"}}, nil
	}
	f.searches++
	return llm.Response{Message: codeSearchToolCallMessage(fmt.Sprintf("search_%d", f.searches), fmt.Sprintf(`{"query":"q%d"}`, f.searches))}, nil
}

type parallelBurstFakeClient struct {
	requests []llm.Request
	turns    int
}

func (f *parallelBurstFakeClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	if f.turns == 0 {
		f.turns++
		return llm.Response{Message: parallelCodeSearchToolCallMessage(10)}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done after parallel burst"}}, nil
}

func TestRunnerAsksForClarificationAfterRepeatedAccessFailures(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: restrictedToolCallMessage("tool_1", `{"target":"prod logs"}`)},
		{Message: restrictedToolCallMessage("tool_2", `{"target":"prod metrics"}`)},
		{Message: restrictedToolCallMessage("tool_3", `{"target":"prod dashboard"}`)},
		{Message: restrictedToolCallMessage("tool_4", `{"target":"prod config"}`)},
		{Message: llm.Message{Role: "assistant", Content: "should not reach this"}},
	}}
	tools := registry.New()
	tools.Register(fakeRestrictedTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 8}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Pending {
		t.Fatal("expected pending clarification")
	}
	if !strings.Contains(result.PendingQuestion, "permission") {
		t.Fatalf("PendingQuestion = %q, want access clarification", result.PendingQuestion)
	}
	if len(client.requests) != 4 {
		t.Fatalf("Chat calls = %d, want stop after fourth access failure", len(client.requests))
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

func repoMissToolCallMessage(id, args string) llm.Message {
	msg := toolCallMessage(id, args)
	msg.ToolCalls[0].Function.Name = "repo-search"
	return msg
}

func restrictedToolCallMessage(id, args string) llm.Message {
	msg := toolCallMessage(id, args)
	msg.ToolCalls[0].Function.Name = "restricted"
	return msg
}

func codeSearchToolCallMessage(id, args string) llm.Message {
	msg := toolCallMessage(id, args)
	msg.ToolCalls[0].Function.Name = "code-search"
	return msg
}

func parallelCodeSearchToolCallMessage(count int) llm.Message {
	calls := make([]llm.ToolCall, count)
	for i := 0; i < count; i++ {
		calls[i] = llm.ToolCall{
			ID:   fmt.Sprintf("parallel_%d", i),
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "code-search",
				Arguments: fmt.Sprintf(`{"query":"parallel-%d"}`, i),
			},
		}
	}
	return llm.Message{Role: "assistant", ToolCalls: calls}
}

func mixedToolCallMessage() llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "p1", Type: "function", Function: llm.ToolFunction{Name: "parallel-before", Arguments: `{}`}},
		{ID: "s1", Type: "function", Function: llm.ToolFunction{Name: "serial-middle", Arguments: `{}`}},
		{ID: "p2", Type: "function", Function: llm.ToolFunction{Name: "parallel-after", Arguments: `{}`}},
	}}
}

func TestRunnerPreservesToolOrderAcrossParallelAndSerialBatches(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: mixedToolCallMessage()},
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	var mu sync.Mutex
	var order []string
	tools := registry.New()
	tools.Register(recordingTool{name: "parallel-before", parallel: true, mu: &mu, order: &order})
	tools.Register(recordingTool{name: "serial-middle", mu: &mu, order: &order})
	tools.Register(recordingTool{name: "parallel-after", parallel: true, mu: &mu, order: &order})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 3}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	got := strings.Join(order, ",")
	want := "parallel-before,serial-middle,parallel-after"
	if got != want {
		t.Fatalf("tool execution order = %s, want %s", got, want)
	}
}

func TestRunnerAppliesToolResultBudgetBeforeModelCall(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	messages := []llm.Message{
		{Role: "system", Content: "system"},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       "tool_1",
				Type:     "function",
				Function: llm.ToolFunction{Name: "echo", Arguments: `{}`},
			}},
		},
		{Role: "tool", Name: "echo", ToolCallID: "tool_1", Content: strings.Repeat("x", maxToolResultChars+1000)},
	}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 1}.Run(context.Background(), Request{
		Messages: messages,
		RunID:    "budget-test",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	found := false
	for _, msg := range client.requests[0].Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "<persisted-output>") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("large historical tool result was not persisted before model call: %#v", client.requests[0].Messages)
	}
}

func TestRunnerRecoversFromMaxOutputTokens(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Message:      llm.Message{Role: "assistant", Content: "partial"},
			FinishReason: "max_tokens",
		},
		{Message: llm.Message{Role: "assistant", Content: "complete"}},
	}}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 3}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "complete" {
		t.Fatalf("Final = %q, want complete", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("Chat calls = %d, want 2", len(client.requests))
	}
	foundRecoveryPrompt := false
	for _, msg := range client.requests[1].Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Output token limit hit") {
			foundRecoveryPrompt = true
			break
		}
	}
	if !foundRecoveryPrompt {
		t.Fatalf("max output token recovery prompt missing from retry: %#v", client.requests[1].Messages)
	}
}

func TestMicroCompactClearsOldToolResults(t *testing.T) {
	c := &memory.Compactor{
		KeepRecentTools: 4,
		ClearableTools:  map[string]bool{"bash": true, "read": true},
	}
	r := Runner{Compactor: c}
	messages := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "question"},
		{Role: "assistant"},
		{Role: "tool", Name: "bash", ToolCallID: "1", Content: strings.Repeat("x", 200)},
		{Role: "assistant"},
		{Role: "tool", Name: "read", ToolCallID: "2", Content: strings.Repeat("y", 200)},
		// recent messages (within last 4 tool results)
		{Role: "assistant"},
		{Role: "tool", Name: "bash", ToolCallID: "3", Content: strings.Repeat("z", 200)},
		{Role: "assistant"},
		{Role: "tool", Name: "read", ToolCallID: "4", Content: "recent"},
		{Role: "assistant", Content: "thinking..."},
		{Role: "tool", Name: "bash", ToolCallID: "5", Content: "latest"},
	}
	result := r.Compactor.ApplyMicroCompact(messages)
	// Old tool results (before boundary) should be cleared
	if result[3].Content != memory.ToolResultClearedMsg {
		t.Fatalf("expected old tool result [1] to be cleared, got: %s", result[3].Content)
	}
	// Recent results (within last 4) should be preserved
	if result[11].Content != "latest" {
		t.Fatalf("expected recent tool result to be preserved, got: %s", result[11].Content)
	}
}

func TestMicroCompactNoOpUnderLimit(t *testing.T) {
	c := &memory.Compactor{
		KeepRecentTools: 8,
		ClearableTools:  map[string]bool{"bash": true},
	}
	r := Runner{Compactor: c}
	messages := []llm.Message{
		{Role: "system", Content: "prompt"},
		{Role: "tool", Name: "bash", ToolCallID: "1", Content: "short"},
	}
	result := r.Compactor.ApplyMicroCompact(messages)
	if result[1].Content != "short" {
		t.Fatal("should not compress when under tool count limit")
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

type fakeStreamClient struct {
	fakeClient
	streamCalls           int
	deltas                []string
	suppressContentDeltas bool
}

func (f *fakeStreamClient) ChatStream(ctx context.Context, req llm.Request, h llm.StreamHandler) (llm.Response, error) {
	f.streamCalls++
	resp, err := f.Chat(ctx, req)
	if err != nil {
		return resp, err
	}
	emitText := func(delta string) {
		if h.OnText != nil {
			h.OnText(delta)
		}
	}
	if len(f.deltas) > 0 {
		for _, delta := range f.deltas {
			emitText(delta)
		}
		f.deltas = nil
	} else if resp.Message.Content != "" && !f.suppressContentDeltas {
		emitText(resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) > 0 && h.OnToolCallsStarted != nil {
		h.OnToolCallsStarted()
	}
	resp.Streamed = true
	return resp, nil
}

func TestRunnerStreamsFinalAnswerWithToolsAvailable(t *testing.T) {
	client := &fakeStreamClient{fakeClient: fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "streamed final"}},
	}}}
	tools := registry.New()
	tools.Register(fakeTool{})

	var narrated, answered strings.Builder
	result, err := Runner{
		LLM:      client,
		Tools:    tools,
		MaxSteps: 3,
		OnStream: func(ev StreamEvent) {
			switch ev.Kind {
			case StreamNarration:
				narrated.WriteString(ev.Delta)
			case StreamAnswer:
				answered.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if client.streamCalls != 1 {
		t.Fatalf("ChatStream calls = %d, want 1", client.streamCalls)
	}
	if len(client.requests) != 1 || len(client.requests[0].Tools) == 0 {
		t.Fatalf("stream request should include tools: %#v", client.requests)
	}
	if result.Final != "streamed final" {
		t.Fatalf("Final = %q, want streamed final", result.Final)
	}
	if result.Streamed != true {
		t.Fatal("result should be marked streamed")
	}
	// Model chose to answer directly (no tool calls) → routed as answer.
	if got := answered.String(); got != "streamed final" {
		t.Fatalf("answer = %q, want streamed final", got)
	}
	if narrated.Len() != 0 {
		t.Fatalf("narration = %q, want empty", narrated.String())
	}
}

func TestRunnerExecutesToolCallsFromStreamResponse(t *testing.T) {
	client := &fakeStreamClient{fakeClient: fakeClient{responses: []llm.Response{
		{Message: toolCallMessage("tool_1", `{"text":"ok"}`)},
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}}
	tools := registry.New()
	tools.Register(fakeTool{})

	var answered strings.Builder
	result, err := Runner{
		LLM:      client,
		Tools:    tools,
		MaxSteps: 2,
		OnStream: func(ev StreamEvent) {
			if ev.Kind == StreamAnswer {
				answered.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if client.streamCalls != 2 {
		t.Fatalf("ChatStream calls = %d, want 2", client.streamCalls)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	toolResults := 0
	for _, msg := range result.Generated {
		if msg.Role == "tool" && msg.ToolCallID == "tool_1" {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool results = %d, want 1", toolResults)
	}
	if got := answered.String(); got != "done" {
		t.Fatalf("answer = %q, want done", got)
	}
}

func TestRunnerBypassesStreamGuardForNativeToolCalls(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: llm.Message{Role: "assistant", Content: "hello world"}},
		}},
		deltas: []string{"hi"},
	}

	var streamed strings.Builder
	_, err := Runner{
		LLM:          client,
		Capabilities: llm.Capabilities{NativeToolCalls: true},
		MaxSteps:     1,
		OnStream:     func(ev StreamEvent) { streamed.WriteString(ev.Delta) },
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := streamed.String(); got != "hi" {
		t.Fatalf("streamed text = %q, want immediate native delta", got)
	}
}

func TestRunnerUsesStreamGuardForUnknownCapabilities(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: llm.Message{Role: "assistant", Content: "normal final"}},
		}},
		deltas: []string{"<tool_call>bad</tool_call>"},
	}

	var streamed strings.Builder
	result, err := Runner{
		LLM:      client,
		MaxSteps: 1,
		OnStream: func(ev StreamEvent) { streamed.WriteString(ev.Delta) },
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := streamed.String(); got != "" {
		t.Fatalf("streamed text = %q, want suppressed textual tool call", got)
	}
	if result.Streamed {
		t.Fatal("result should not be marked streamed when guard suppresses output")
	}
}

func TestRunnerRoutesPostToolToolRoundToNarration(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: toolCallMessage("tool_1", `{"text":"ok"}`)},
			{Message: llm.Message{
				Role:    "assistant",
				Content: "让我看看仓库里现有的写法...",
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_2",
					Type: "function",
					Function: llm.ToolFunction{
						Name:      "echo",
						Arguments: `{"text":"ok2"}`,
					},
				}},
			}},
			{Message: llm.Message{Role: "assistant", Content: "done"}},
		}},
		deltas: []string{"让我先看看这个 PR 的内容..."},
	}
	tools := registry.New()
	tools.Register(fakeTool{})

	var narrated, answered strings.Builder
	_, err := Runner{
		LLM:          client,
		Tools:        tools,
		Capabilities: llm.Capabilities{NativeToolCalls: true},
		MaxSteps:     12,
		OnStream: func(ev StreamEvent) {
			switch ev.Kind {
			case StreamNarration:
				narrated.WriteString(ev.Delta)
			case StreamAnswer:
				answered.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(narrated.String(), "让我先看看这个 PR") {
		t.Fatalf("narration = %q, want first round preamble", narrated.String())
	}
	if !strings.Contains(narrated.String(), "让我看看仓库里") {
		t.Fatalf("narration = %q, want second round preamble", narrated.String())
	}
	if !strings.Contains(narrated.String(), "让我先看看这个 PR 的内容...\n\n让我看看仓库里") {
		t.Fatalf("narration = %q, want paragraph break between tool rounds", narrated.String())
	}
	if got := answered.String(); got != "done" {
		t.Fatalf("answer = %q, want final only on answer stream", got)
	}
}

func TestRunnerRoutesPostToolFinalAnswerToAnswer(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: toolCallMessage("tool_1", `{"text":"ok"}`)},
			{Message: llm.Message{Role: "assistant", Content: "final answer"}},
		}},
		deltas: []string{"preamble"},
	}
	tools := registry.New()
	tools.Register(fakeTool{})

	var narrated, answered strings.Builder
	_, err := Runner{
		LLM:          client,
		Tools:        tools,
		Capabilities: llm.Capabilities{NativeToolCalls: true},
		MaxSteps:     12,
		OnStream: func(ev StreamEvent) {
			switch ev.Kind {
			case StreamNarration:
				narrated.WriteString(ev.Delta)
			case StreamAnswer:
				answered.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := narrated.String(); got != "preamble" {
		t.Fatalf("narration = %q, want preamble", got)
	}
	if got := answered.String(); got != "final answer" {
		t.Fatalf("answer = %q, want final answer", got)
	}
}

func TestRunnerNarratesStreamedToolPreamble(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: toolCallMessage("tool_1", `{"text":"ok"}`)},
			{Message: llm.Message{Role: "assistant", Content: "done"}},
		}},
		deltas: []string{"让我看看相关代码。"},
	}
	tools := registry.New()
	tools.Register(fakeTool{})

	var narrated, answered strings.Builder
	result, err := Runner{
		LLM:          client,
		Tools:        tools,
		Capabilities: llm.Capabilities{NativeToolCalls: true},
		MaxSteps:     2,
		OnStream: func(ev StreamEvent) {
			switch ev.Kind {
			case StreamNarration:
				narrated.WriteString(ev.Delta)
			case StreamAnswer:
				answered.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := narrated.String(); got != "让我看看相关代码。" {
		t.Fatalf("narration = %q, want streamed preamble", got)
	}
	if got := answered.String(); got != "done" {
		t.Fatalf("answer stream = %q, want done", got)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	if !result.Streamed {
		t.Fatal("result should be marked streamed")
	}
}

func TestRunnerNarratesToolPreambleWhenStreamDoesNotEmitContent(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: llm.Message{
				Role:    "assistant",
				Content: "让我看看相关代码。",
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
		}},
		suppressContentDeltas: true,
	}
	tools := registry.New()
	tools.Register(fakeTool{})

	var narrated strings.Builder
	result, err := Runner{
		LLM:          client,
		Tools:        tools,
		Capabilities: llm.Capabilities{NativeToolCalls: true},
		MaxSteps:     3,
		OnStream: func(ev StreamEvent) {
			if ev.Kind == StreamNarration {
				narrated.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "done" {
		t.Fatalf("Final = %q, want done", result.Final)
	}
	if got := narrated.String(); got != "让我看看相关代码。" {
		t.Fatalf("narration = %q, want preamble", got)
	}
}

func TestRunnerSeparatesThreeToolRoundsWithoutExtraNewlines(t *testing.T) {
	client := &fakeStreamClient{
		fakeClient: fakeClient{responses: []llm.Response{
			{Message: toolCallMessage("tool_1", `{"text":"ok"}`)},
			{Message: llm.Message{
				Role:    "assistant",
				Content: "round two",
				ToolCalls: []llm.ToolCall{{
					ID: "tool_2", Type: "function",
					Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"ok2"}`},
				}},
			}},
			{Message: llm.Message{
				Role:    "assistant",
				Content: "round three",
				ToolCalls: []llm.ToolCall{{
					ID: "tool_3", Type: "function",
					Function: llm.ToolFunction{Name: "echo", Arguments: `{"text":"ok3"}`},
				}},
			}},
			{Message: llm.Message{Role: "assistant", Content: "done"}},
		}},
		deltas: []string{"round one"},
	}
	tools := registry.New()
	tools.Register(fakeTool{})

	var narrated strings.Builder
	_, err := Runner{
		LLM:          client,
		Tools:        tools,
		Capabilities: llm.Capabilities{NativeToolCalls: true},
		MaxSteps:     12,
		OnStream: func(ev StreamEvent) {
			if ev.Kind == StreamNarration {
				narrated.WriteString(ev.Delta)
			}
		},
	}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := narrated.String()
	if strings.Contains(got, "\n\n\n\n") {
		t.Fatalf("narration = %q, want single paragraph breaks only", got)
	}
	want := "round one\n\nround two\n\nround three"
	if got != want {
		t.Fatalf("narration = %q, want %q", got, want)
	}
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

func TestRunnerReturnsErrorWhenFinalResponseStaysEmpty(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant"}},
		{Message: llm.Message{Role: "assistant"}},
	}}

	result, err := Runner{LLM: client, Tools: registry.New(), MaxSteps: 3}.Run(context.Background(), Request{})
	if !errors.Is(err, ErrEmptyFinal) {
		t.Fatalf("Run() error = %v, want ErrEmptyFinal", err)
	}
	if result.Final != "" {
		t.Fatalf("Final = %q, want empty failed result", result.Final)
	}
	if len(result.Generated) != 0 {
		t.Fatalf("empty final should not be persisted as generated answer: %#v", result.Generated)
	}
	if len(client.requests) != 2 {
		t.Fatalf("Chat calls = %d, want one retry", len(client.requests))
	}
}

func TestRunnerPassesAllActiveToolSpecsWithoutIntentKeywordPruning(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	tools := registry.New()
	activeNames := []string{
		"tool_search",
		"plan-update",
		"code-search",
		"code-read_file",
		"repo-search",
		"repo-read_file",
		"readonly-shell",
		"gcp-logs",
		"k8s-get_pods",
		"k8s-describe",
		"k8s-logs",
		"k8s-top",
		"github-workflow_runs",
		"github-job_logs",
		"slack-file_search",
		"web-search",
		"rag-search",
		"delegate-run",
		"explore-code",
	}
	for _, name := range activeNames {
		tools.Register(fakeNamedTool{name: name})
	}
	tools.RegisterDeferred(registry.AsDeferred(registry.CategoryBrowser, fakeNamedTool{name: "pw-snapshot"}))

	_, err := Runner{LLM: client, Tools: tools, MaxSteps: 1}.Run(context.Background(), Request{UserQuestion: "服务"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("Chat calls = %d, want 1", len(client.requests))
	}
	got := client.requests[0].Tools
	for _, name := range activeNames {
		if !hasSpec(got, name) {
			t.Fatalf("active tool %s should be exposed regardless of user wording: %v", name, specNames(got))
		}
	}
	if hasSpec(got, "pw-snapshot") {
		t.Fatalf("deferred tool should not be exposed before activation: %v", specNames(got))
	}
}

func hasSpec(specs []llm.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Function.Name == name {
			return true
		}
	}
	return false
}

func specNames(specs []llm.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Function.Name)
	}
	return names
}

func TestRunnerKeepsActiveToolsVisibleAfterToolFailure(t *testing.T) {
	client := &exploreFailureAwareClient{}
	tools := registry.New()
	tools.Register(fakeExploreErrorTool{})
	tools.Register(fakeCodeSearchTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 8}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "continued without explore" {
		t.Fatalf("Final = %q, want continued without explore", result.Final)
	}
	lastReq := client.requests[len(client.requests)-1]
	if !hasSpec(lastReq.Tools, "explore-code") {
		t.Fatalf("active tool should remain visible after failures: %v", specNames(lastReq.Tools))
	}
}

type exploreFailureAwareClient struct {
	requests []llm.Request
	calls    int
}

func (f *exploreFailureAwareClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	f.calls++
	if f.calls <= 3 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID: "explore_" + fmt.Sprint(f.calls), Type: "function",
			Function: llm.ToolFunction{Name: "explore-code", Arguments: `{"task":"broad investigation"}`},
		}}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "continued without explore"}}, nil
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

func TestRunnerRetriesOnFileLineClaimWithoutCodeTool(t *testing.T) {
	answer := "The issue is in internal/app/runtime.go:45 where the runner is wired."
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: answer}},
		{Message: llm.Message{Role: "assistant", Content: "I need to read the file before making that claim."}},
	}}
	tools := registry.New()
	tools.Register(fakeTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "I need to read the file before making that claim." {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(client.requests))
	}
}

func TestRunnerDoesNotTreatRAGSearchAsCodeEvidence(t *testing.T) {
	answer := "The guard is in internal/app/runtime.go:45."
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID:   "rag_1",
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "rag-search",
				Arguments: `{"query":"runtime guard"}`,
			},
		}}}},
		{Message: llm.Message{Role: "assistant", Content: answer}},
		{Message: llm.Message{Role: "assistant", Content: "I need to read the file before making that claim."}},
	}}
	tools := registry.New()
	tools.Register(fakeRAGSearchTool{})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 5}.Run(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "I need to read the file before making that claim." {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected 3 LLM calls (rag, retry, final), got %d", len(client.requests))
	}
}

func TestRunnerDoesNotKeywordGatePlanOrEvidence(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "这是一个不含代码位置声明的直接结论。"}},
	}}
	tools := registry.New()
	tools.Register(fakeNamedTool{name: "plan-update"})
	tools.Register(fakeNamedTool{name: "code-search"})

	result, err := Runner{LLM: client, Tools: tools, MaxSteps: 8}.Run(context.Background(), Request{
		UserQuestion: "为什么登录报错，帮我查 bug，并对比 agent 架构和准确性问题",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Final != "这是一个不含代码位置声明的直接结论。" {
		t.Fatalf("Final = %q", result.Final)
	}
	if len(client.requests) != 1 {
		t.Fatalf("lexical plan/evidence gates should not force retries; got %d calls", len(client.requests))
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
	return namedToolCallMessage(id, "git-read_file_ref", args)
}

func namedToolCallMessage(id, name, args string) llm.Message {
	return llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:   id,
			Type: "function",
			Function: llm.ToolFunction{
				Name:      name,
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

type fakeRAGSearchTool struct{}

func (fakeRAGSearchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "rag-search",
			Description: "search semantic code index",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeRAGSearchTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: "semantic hit: " + string(args)}, nil
}

type fakeNamedTool struct {
	name string
}

func (t fakeNamedTool) Parallel() bool { return true }

func (t fakeNamedTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        t.name,
			Description: "fake named tool",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (t fakeNamedTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: t.name + ": " + string(args)}, nil
}

type recordingTool struct {
	name     string
	parallel bool
	mu       *sync.Mutex
	order    *[]string
}

func (t recordingTool) Parallel() bool { return t.parallel }

func (t recordingTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        t.name,
			Description: "record execution order",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (t recordingTool) Execute(_ context.Context, _ json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	t.mu.Lock()
	*t.order = append(*t.order, t.name)
	t.mu.Unlock()
	return registry.Result{Content: t.name}, nil
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

func lastSystemContent(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "system" {
			return messages[i].Content
		}
	}
	return ""
}

type fakeMissingWorkspaceTool struct{}

func (fakeMissingWorkspaceTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "repo-search",
			Description: "search repository",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeMissingWorkspaceTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var payload struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &payload)
	if payload.Path == "" {
		payload.Path = "repo"
	}
	return registry.Result{}, fmt.Errorf("file not found in any workspace root: %s", payload.Path)
}

type fakeRestrictedTool struct{}

func (fakeRestrictedTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "restricted",
			Description: "restricted data source",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeRestrictedTool) Execute(_ context.Context, _ json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{}, errors.New("permission denied: missing credentials")
}

type fakeCodeSearchTool struct{}

func (fakeCodeSearchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "code-search",
			Description: "search code",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeCodeSearchTool) Execute(_ context.Context, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: string(args)}, nil
}

type countingSlowTool struct {
	calls *int32
}

func (countingSlowTool) Parallel() bool { return true }

func (countingSlowTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "slow",
			Description: "slow read-only tool",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (t countingSlowTool) Execute(context.Context, json.RawMessage, registry.Runtime) (registry.Result, error) {
	atomic.AddInt32(t.calls, 1)
	time.Sleep(25 * time.Millisecond)
	return registry.Result{Content: "ok"}, nil
}

type fakeExploreErrorTool struct{}

func (fakeExploreErrorTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        "explore-code",
			Description: "explore code",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (fakeExploreErrorTool) Execute(_ context.Context, _ json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	return registry.Result{}, errors.New("explore did not produce a text report after synthesis retry")
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
	return registry.Result{Content: args.Question, NeedsUserInput: true, WaitForUser: true}, nil
}

type fakeObservationFormatter struct{}

func (fakeObservationFormatter) ToolObservation(toolName string, output string) string {
	return "<evidence source=\"" + toolName + "\">\n" + output + "\n</evidence>"
}

func TestStreamGuardPreservesUTF8AcrossFlushes(t *testing.T) {
	// Build text long enough to trigger incremental flushes with a CJK tail.
	text := strings.Repeat("已有的 CommentReplyRuleProcessor 模式，", 20)
	var chunks []string
	guard := &streamGuard{downstream: func(delta string) { chunks = append(chunks, delta) }}
	for _, r := range text {
		guard.Write(string(r))
	}
	guard.Flush()

	got := strings.Join(chunks, "")
	if got != text {
		t.Fatalf("stream guard corrupted UTF-8:\nwant %q\ngot  %q", text, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
	if strings.Contains(got, "\uFFFD") {
		t.Fatalf("output contains replacement characters: %q", got)
	}
}

func TestUTF8SafeCut(t *testing.T) {
	s := "AutomationRule（Team-Inbox）"
	for cut := 1; cut < len(s); cut++ {
		n := utf8SafeCut(s, cut)
		if !utf8.ValidString(s[:n]) {
			t.Fatalf("utf8SafeCut(%q, %d) = %d, invalid prefix %q", s, cut, n, s[:n])
		}
	}
}

// TestUseStreamGuard verifies that RepairTextualToolCalls takes priority over NativeToolCalls
// so that the streaming guard is active even for providers that support structured tool calls.
// Regression test for: NativeToolCalls short-circuiting RepairTextualToolCalls check,
// causing textual tool call markup to be streamed to the user undetected.
func TestUseStreamGuard(t *testing.T) {
	cases := []struct {
		name      string
		caps      llm.Capabilities
		wantGuard bool
	}{
		{
			name:      "RepairTextual wins over NativeToolCalls",
			caps:      llm.Capabilities{NativeToolCalls: true, RepairTextualToolCalls: true},
			wantGuard: true,
		},
		{
			name:      "RepairTextual alone enables guard",
			caps:      llm.Capabilities{NativeToolCalls: false, RepairTextualToolCalls: true},
			wantGuard: true,
		},
		{
			name:      "NativeToolCalls alone disables guard",
			caps:      llm.Capabilities{NativeToolCalls: true, RepairTextualToolCalls: false},
			wantGuard: false,
		},
		{
			name:      "both false, unknown provider enables guard",
			caps:      llm.Capabilities{Provider: "", Protocol: ""},
			wantGuard: true,
		},
		{
			name:      "both false, known provider disables guard",
			caps:      llm.Capabilities{Provider: "openai", Protocol: "openai"},
			wantGuard: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Runner{Capabilities: tc.caps}
			if got := r.useStreamGuard(); got != tc.wantGuard {
				t.Errorf("useStreamGuard() = %v, want %v", got, tc.wantGuard)
			}
		})
	}
}
