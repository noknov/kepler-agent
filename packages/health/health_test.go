package health

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

func TestServiceReportsMissingCriticalTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	reg.Register(fakeTool{name: "code-search"})

	service := NewService(reg, []string{root})
	snap := service.Probe(context.Background())

	if snap.Overall != StatusUnhealthy {
		t.Fatalf("Overall = %s, want unhealthy", snap.Overall)
	}
	if !strings.Contains(service.SummaryPrompt(), "repo-search") {
		t.Fatalf("SummaryPrompt() should mention missing repo-search, got %q", service.SummaryPrompt())
	}
}

type fakeTool struct {
	name string
}

func (t fakeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolSpecFunction{
			Name:        t.name,
			Description: "fake",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func (t fakeTool) Execute(context.Context, json.RawMessage, registry.Runtime) (registry.Result, error) {
	return registry.Result{Content: "ok"}, nil
}
