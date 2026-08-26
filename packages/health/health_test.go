package health

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestServiceReportsMissingCriticalTools(t *testing.T) {
	root := t.TempDir()
	reg, err := agenttool.NewCatalog(fakeTool{name: "code-search"})
	if err != nil {
		t.Fatal(err)
	}

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

func (t fakeTool) Descriptor() agenttool.Descriptor {
	return agenttool.Descriptor{Name: t.name, Description: "fake", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []agenttool.Effect{agenttool.EffectRead}}
}

func (t fakeTool) Execute(context.Context, agenttool.Call) (agenttool.Result, error) {
	return agenttool.Result{Content: []model.Content{{Type: model.ContentText, Text: "ok"}}}, nil
}
