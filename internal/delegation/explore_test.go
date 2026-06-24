package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
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

func TestExploreRetriesTextReportWhenFinalStepRequestsTools(t *testing.T) {
	responses := make([]llm.Response, 0, exploreMaxSteps+1)
	for i := 0; i < exploreMaxSteps-1; i++ {
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
	if len(client.requests) != exploreMaxSteps+1 {
		t.Fatalf("Chat calls = %d, want final retry", len(client.requests))
	}
	if len(client.requests[exploreMaxSteps-1].Tools) != 0 || len(client.requests[exploreMaxSteps].Tools) != 0 {
		t.Fatal("final synthesis and retry requests should not expose tools")
	}
	if tools.calls != exploreMaxSteps-1 {
		t.Fatalf("tool calls = %d, want no execution for final-step tool request", tools.calls)
	}
	foundRetry := false
	for _, msg := range client.requests[exploreMaxSteps].Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Tool calls are no longer available") {
			foundRetry = true
			break
		}
	}
	if !foundRetry {
		t.Fatal("synthesis retry prompt was not injected into explore retry")
	}
}

func TestExploreReturnsPartialReportWhenSynthesisRetryStillRequestsTools(t *testing.T) {
	responses := make([]llm.Response, 0, exploreMaxSteps+1)
	for i := 0; i < exploreMaxSteps-1; i++ {
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
	if tools.calls != exploreMaxSteps-1 {
		t.Fatalf("tool calls = %d, want no execution for synthesis retry tool calls", tools.calls)
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
