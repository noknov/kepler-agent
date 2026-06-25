package registry

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wati/oncall-agent/internal/llm"
)

type stubTool struct {
	name string
}

func (s stubTool) Spec() llm.ToolSpec {
	return FunctionSpec(s.name, "stub", ObjectSchema(nil, map[string]any{}))
}

func (s stubTool) Execute(context.Context, json.RawMessage, Runtime) (Result, error) {
	return Result{Content: "ok"}, nil
}

func TestRegisterDeferredExcludesFromSpecsUntilActivated(t *testing.T) {
	reg := New()
	reg.Register(stubTool{name: "active-tool"})
	reg.RegisterDeferred(AsDeferred(CategoryDiagnostics, stubTool{name: "diag-tool"}))
	reg.Register(ToolSearchTool{Registry: reg})

	specs := reg.Specs()
	if len(specs) != 2 {
		t.Fatalf("Specs() len = %d, want 2 (active-tool + tool_search)", len(specs))
	}
	for _, spec := range specs {
		if spec.Function.Name == "diag-tool" {
			t.Fatal("deferred tool should not appear in default Specs()")
		}
	}

	activated := reg.ActivateCategory(CategoryDiagnostics)
	if len(activated) != 1 || activated[0] != "diag-tool" {
		t.Fatalf("ActivateCategory() = %v", activated)
	}
	if len(reg.Specs()) != 3 {
		t.Fatalf("Specs() after activation len = %d, want 3", len(reg.Specs()))
	}
}

func TestToolSearchListAndActivate(t *testing.T) {
	reg := New()
	reg.RegisterDeferred(AsDeferred(CategoryBrowser, stubTool{name: "pw-snapshot"}))
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubTool{name: "notion-search"}))
	search := ToolSearchTool{Registry: reg}

	list, err := search.Execute(context.Background(), json.RawMessage(`{"action":"list"}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Content == "" {
		t.Fatal("expected list output")
	}

	activate, err := search.Execute(context.Background(), json.RawMessage(`{"action":"activate","categories":["browser"]}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if activate.Content == "" || !reg.Has("pw-snapshot") {
		t.Fatalf("activate result = %q, Has(pw-snapshot) = %v", activate.Content, reg.Has("pw-snapshot"))
	}
	if reg.Has("notion-search") {
		t.Fatal("notion-search should remain deferred")
	}
}
