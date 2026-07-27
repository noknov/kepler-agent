package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/llm"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
)

func TestExploreUsesOnlyReadOnlyToolsAndReturnsReport(t *testing.T) {
	client := &exploreFakeClient{responses: []llm.Response{
		{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "search_1", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"Catalog"}`}},
				{ID: "read_1", Type: "function", Function: llm.ToolFunction{Name: "code-read_file", Arguments: `{"path":"Catalog/index.tsx"}`}},
			},
		}},
		{Message: llm.Message{Role: "assistant", Content: "Finding: compare entry points.\nEvidence:\n- Catalog/index.tsx:1 mounts Catalog.\nExcluded: external API path."}},
	}}
	tools := &exploreFakeTools{}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(tools)

	out, err := manager.Explore(context.Background(), "compare catalog entry points", "repo frontend", registry.Runtime{})
	if err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if !strings.HasPrefix(out, "Finding:") {
		t.Fatalf("Explore() output = %q", out)
	}
	if len(client.requests) != 2 {
		t.Fatalf("Chat calls = %d, want 2", len(client.requests))
	}
	if len(client.requests[0].Tools) != 3 {
		t.Fatalf("explore should expose only read-only tools, got %d", len(client.requests[0].Tools))
	}
	foundDiagnostics := false
	for _, spec := range client.requests[0].Tools {
		if spec.Function.Name == "delegate-run" || spec.Function.Name == "notion-create_page" {
			t.Fatalf("unsafe/non-explore tool exposed: %s", spec.Function.Name)
		}
		if spec.Function.Name == "code-diagnostics" {
			foundDiagnostics = true
		}
	}
	if !foundDiagnostics {
		t.Fatal("code-diagnostics was not exposed to explore")
	}
	if len(client.requests[1].Tools) != 3 {
		t.Fatalf("second request should still expose read-only tools before final step")
	}
	if got := tools.calls; got != 2 {
		t.Fatalf("tool calls = %d, want 2", got)
	}
}

func TestExploreRejectsUnavailableTools(t *testing.T) {
	client := &exploreFakeClient{responses: []llm.Response{
		{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "bad_1", Type: "function", Function: llm.ToolFunction{Name: "notion-create_page", Arguments: `{}`}},
			},
		}},
		{Message: llm.Message{Role: "assistant", Content: "Finding: unsafe tool was unavailable."}},
	}}
	tools := &exploreFakeTools{}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(tools)

	out, err := manager.Explore(context.Background(), "try unsafe tool", "", registry.Runtime{})
	if err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if out == "" {
		t.Fatal("expected final report")
	}
	if tools.calls != 0 {
		t.Fatalf("unsafe tool should not execute, calls = %d", tools.calls)
	}
}

func TestExploreProfileRestrictsAllowedTools(t *testing.T) {
	client := &exploreFakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "Finding: search only."}},
	}}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(&exploreFakeTools{})
	manager.SetExploreProfile(ExploreProfile{
		MaxSteps:     4,
		Parallelism:  2,
		AllowedTools: map[string]bool{"code-search": true},
		SystemPrompt: "explore",
		FinalPrompt:  "final",
	})

	if _, err := manager.Explore(context.Background(), "search only", "", registry.Runtime{}); err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if got := len(client.requests[0].Tools); got != 1 {
		t.Fatalf("Tools len = %d, want 1", got)
	}
	if name := client.requests[0].Tools[0].Function.Name; name != "code-search" {
		t.Fatalf("tool = %q, want code-search", name)
	}
}

func TestExploreToolRunsBatchedTasksInParallel(t *testing.T) {
	client := &exploreBatchFakeClient{delay: 50 * time.Millisecond}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(&exploreFakeTools{})
	manager.SetExploreProfile(ExploreProfile{
		MaxSteps:     4,
		Parallelism:  2,
		MaxWorkers:   2,
		AllowedTools: map[string]bool{"code-search": true},
		SystemPrompt: "explore",
		FinalPrompt:  "final",
	})
	tool := ExploreTool{Manager: manager}
	if !tool.Parallel() {
		t.Fatal("explore-code should be runner-parallel so multiple calls can run concurrently")
	}

	start := time.Now()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"task":"find routes"},{"task":"find storage"}]}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 95*time.Millisecond {
		t.Fatalf("batched explorers took %v, expected parallel execution under 95ms", elapsed)
	}
	if client.maxConcurrent < 2 {
		t.Fatalf("maxConcurrent = %d, want 2", client.maxConcurrent)
	}
	if !strings.Contains(result.Content, "Explorer 1: find routes") || !strings.Contains(result.Content, "Explorer 2: find storage") {
		t.Fatalf("combined report = %q", result.Content)
	}
}

func TestExploreRetriesTextReportWhenFinalStepRequestsTools(t *testing.T) {
	responses := make([]llm.Response, 0, exploreMaxSteps)
	for i := 0; i < exploreMaxSteps-2; i++ {
		responses = append(responses, llm.Response{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "search_" + string(rune('a'+i)), Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"Catalog"}`}},
			},
		}})
	}
	responses = append(responses,
		llm.Response{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "late_tool", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"too-late"}`}},
			},
		}},
		llm.Response{Message: llm.Message{Role: "assistant", Content: "Finding: final text report."}},
	)
	client := &exploreFakeClient{responses: responses}
	tools := &exploreFakeTools{}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(tools)

	out, err := manager.Explore(context.Background(), "broad search", "", registry.Runtime{})
	if err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if out != "Finding: final text report." {
		t.Fatalf("Explore() output = %q", out)
	}
	if len(client.requests) != exploreMaxSteps {
		t.Fatalf("Chat calls = %d, want %d synthesis attempts after tool rounds", len(client.requests), exploreMaxSteps)
	}
	if len(client.requests[exploreMaxSteps-2].Tools) != 0 || len(client.requests[exploreMaxSteps-1].Tools) != 0 {
		t.Fatal("final synthesis requests should not expose tools")
	}
	if tools.calls != exploreMaxSteps-2 {
		t.Fatalf("tool calls = %d, want no execution for synthesis-step tool requests", tools.calls)
	}
	foundRetry := false
	for _, msg := range client.requests[exploreMaxSteps-1].Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Tool calls are no longer available") {
			foundRetry = true
			break
		}
	}
	if !foundRetry {
		t.Fatal("synthesis retry prompt was not injected before final text report")
	}
}

func TestExploreReturnsPartialReportWhenSynthesisRetryStillRequestsTools(t *testing.T) {
	responses := make([]llm.Response, 0, exploreMaxSteps)
	for i := 0; i < exploreMaxSteps-2; i++ {
		responses = append(responses, llm.Response{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "search_" + string(rune('a'+i)), Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"Catalog"}`}},
			},
		}})
	}
	responses = append(responses,
		llm.Response{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "late_tool", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"too-late"}`}},
			},
		}},
		llm.Response{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "still_late", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"still-too-late"}`}},
			},
		}},
	)
	client := &exploreFakeClient{responses: responses}
	tools := &exploreFakeTools{}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(tools)

	out, err := manager.Explore(context.Background(), "broad search", "", registry.Runtime{})
	if err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if !strings.Contains(out, "partial evidence") || !strings.Contains(out, "code-search") {
		t.Fatalf("Explore() output = %q, want partial evidence report", out)
	}
	if tools.calls != exploreMaxSteps-2 {
		t.Fatalf("tool calls = %d, want no execution for synthesis-step tool calls", tools.calls)
	}
}

func TestExploreUsesChatStreamWhenAvailable(t *testing.T) {
	client := &exploreStreamFakeClient{responses: []llm.Response{
		{Message: llm.Message{Role: "assistant", Content: "Finding: streamed report."}},
	}}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(&exploreFakeTools{})

	out, err := manager.Explore(context.Background(), "quick lookup", "", registry.Runtime{})
	if err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if out != "Finding: streamed report." {
		t.Fatalf("Explore() output = %q", out)
	}
	if client.streamCalls != 1 {
		t.Fatalf("ChatStream calls = %d, want 1", client.streamCalls)
	}
	if client.chatCalls != 0 {
		t.Fatalf("Chat calls = %d, want 0", client.chatCalls)
	}
}

func TestExploreMicroCompactClearsOldToolResults(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "task"},
	}
	for i := 0; i < 8; i++ {
		messages = append(messages,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c" + string(rune('a'+i)), Function: llm.ToolFunction{Name: "code-search"}}}},
			llm.Message{Role: "tool", ToolCallID: "c" + string(rune('a'+i)), Name: "code-search", Content: "result-" + string(rune('a'+i))},
		)
	}
	result := applyExploreMicroCompact(messages)
	cleared := 0
	preserved := 0
	for _, msg := range result {
		if msg.Role != "tool" {
			continue
		}
		if msg.Content == exploreToolResultClearedMsg {
			cleared++
		} else {
			preserved++
		}
	}
	if cleared != 4 {
		t.Fatalf("cleared = %d, want 4 older tool results removed", cleared)
	}
	if preserved != 4 {
		t.Fatalf("preserved = %d, want 4 recent tool results kept", preserved)
	}
}

func TestExploreMicroCompactNoOpWhenFewTools(t *testing.T) {
	messages := []llm.Message{
		{Role: "tool", Name: "code-search", Content: "keep me"},
	}
	result := applyExploreMicroCompact(messages)
	if result[0].Content != "keep me" {
		t.Fatalf("content = %q, want unchanged", result[0].Content)
	}
}

func TestExploreExecutesParallelToolCalls(t *testing.T) {
	client := &exploreFakeClient{responses: []llm.Response{
		{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "search_1", Type: "function", Function: llm.ToolFunction{Name: "code-search", Arguments: `{"query":"A"}`}},
				{ID: "read_1", Type: "function", Function: llm.ToolFunction{Name: "code-read_file", Arguments: `{"path":"a.go"}`}},
			},
		}},
		{Message: llm.Message{Role: "assistant", Content: "Finding: parallel reads done."}},
	}}
	tools := &exploreParallelFakeTools{delay: 50 * time.Millisecond}
	manager := NewManager(client, "test-model", "")
	manager.SetTools(tools)

	start := time.Now()
	out, err := manager.Explore(context.Background(), "parallel reads", "", registry.Runtime{})
	if err != nil {
		t.Fatalf("Explore() error = %v", err)
	}
	if !strings.HasPrefix(out, "Finding:") {
		t.Fatalf("Explore() output = %q", out)
	}
	if elapsed := time.Since(start); elapsed > 120*time.Millisecond {
		t.Fatalf("parallel tool calls took %v, expected under 120ms", elapsed)
	}
	if tools.maxConcurrent < 2 {
		t.Fatalf("maxConcurrent = %d, want at least 2", tools.maxConcurrent)
	}
}

type exploreFakeClient struct {
	responses []llm.Response
	requests  []llm.Request
}

func (f *exploreFakeClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("unexpected chat call")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

type exploreStreamFakeClient struct {
	responses   []llm.Response
	streamCalls int
	chatCalls   int
}

func (f *exploreStreamFakeClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	f.chatCalls++
	return llm.Response{}, errors.New("unexpected chat call")
}

func (f *exploreStreamFakeClient) ChatStream(_ context.Context, _ llm.Request, _ llm.StreamHandler) (llm.Response, error) {
	f.streamCalls++
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("unexpected stream call")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

type exploreBatchFakeClient struct {
	mu            sync.Mutex
	requests      []llm.Request
	active        int
	maxConcurrent int
	delay         time.Duration
}

func (f *exploreBatchFakeClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxConcurrent {
		f.maxConcurrent = f.active
	}
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	time.Sleep(f.delay)

	f.mu.Lock()
	f.active--
	f.mu.Unlock()

	task := "unknown"
	if len(req.Messages) > 1 {
		task = strings.TrimPrefix(req.Messages[1].Content, "Investigation task:\n")
		if idx := strings.Index(task, "\n\nBoundaries:\n"); idx >= 0 {
			task = task[:idx]
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "Finding: " + task}}, nil
}

type exploreParallelFakeTools struct {
	mu            sync.Mutex
	active        int
	maxConcurrent int
	delay         time.Duration
}

func (f *exploreParallelFakeTools) Specs() []llm.ToolSpec {
	return []llm.ToolSpec{
		{Type: "function", Function: llm.ToolSpecFunction{Name: "code-search", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: llm.ToolSpecFunction{Name: "code-read_file", Parameters: map[string]any{"type": "object"}}},
	}
}

func (f *exploreParallelFakeTools) Execute(_ context.Context, name string, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxConcurrent {
		f.maxConcurrent = f.active
	}
	f.mu.Unlock()
	time.Sleep(f.delay)
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return registry.Result{Content: name + ":" + string(args)}, nil
}

func (f *exploreParallelFakeTools) CanRunInParallel(_ string) bool { return false }

type exploreFakeTools struct {
	mu    sync.Mutex
	calls int
}

func (f *exploreFakeTools) Specs() []llm.ToolSpec {
	return []llm.ToolSpec{
		{Type: "function", Function: llm.ToolSpecFunction{Name: "code-search", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: llm.ToolSpecFunction{Name: "code-read_file", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: llm.ToolSpecFunction{Name: "code-diagnostics", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: llm.ToolSpecFunction{Name: "delegate-run", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: llm.ToolSpecFunction{Name: "notion-create_page", Parameters: map[string]any{"type": "object"}}},
	}
}

func (f *exploreFakeTools) Execute(_ context.Context, name string, args json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return registry.Result{Content: name + ":" + string(args)}, nil
}

func (f *exploreFakeTools) CanRunInParallel(name string) bool {
	return name == "code-search" || name == "code-read_file" || name == "code-diagnostics"
}
