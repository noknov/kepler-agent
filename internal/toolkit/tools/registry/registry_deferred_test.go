package registry

import (
	"context"
	"encoding/json"
	"strings"
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

type stubWriteTool struct {
	stubTool
}

func (stubWriteTool) IsWrite() bool { return true }

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

func TestReadOnlyRegistryDoesNotExposeWriteTools(t *testing.T) {
	reg := NewReadOnly()
	reg.Register(stubTool{name: "read-tool"})
	reg.Register(stubWriteTool{stubTool{name: "write-tool"}})
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubWriteTool{stubTool{name: "deferred-write-tool"}}))
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubTool{name: "deferred-read-tool"}))

	for _, spec := range reg.Specs() {
		if strings.Contains(spec.Function.Name, "write") {
			t.Fatalf("read-only specs exposed write tool: %#v", reg.Specs())
		}
	}
	if names := reg.DeferredToolNames(CategoryIntegration); len(names) != 1 || names[0] != "deferred-read-tool" {
		t.Fatalf("DeferredToolNames() = %#v, want only deferred-read-tool", names)
	}
	if reg.ActivateTool("deferred-write-tool") {
		t.Fatal("read-only registry should not activate deferred write tools")
	}
	if !reg.ActivateTool("deferred-read-tool") {
		t.Fatal("read-only registry should activate deferred read tools")
	}
	if _, err := reg.Execute(context.Background(), "write-tool", nil, Runtime{}); err == nil {
		t.Fatal("execution should still refuse hidden write tool if called directly")
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

func TestToolSearchFindsAndActivatesSpecificDeferredTool(t *testing.T) {
	reg := New()
	reg.Register(stubTool{name: "code-search"})
	reg.RegisterDeferred(AsDeferred(CategoryBrowser, stubTool{name: "playwright-screenshot"}))
	search := ToolSearchTool{Registry: reg}

	result, err := search.Execute(context.Background(), json.RawMessage(`{"action":"search","query":"browser screenshot","limit":5}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "playwright-screenshot") {
		t.Fatalf("search result = %q, want playwright-screenshot", result.Content)
	}
	if reg.Has("playwright-screenshot") {
		t.Fatal("search should not activate deferred tools")
	}

	activated, err := search.Execute(context.Background(), json.RawMessage(`{"action":"activate","tool_names":["playwright-screenshot"]}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(activated.Content, "playwright-screenshot") || !reg.Has("playwright-screenshot") {
		t.Fatalf("activate result = %q, Has = %v", activated.Content, reg.Has("playwright-screenshot"))
	}
}

func TestToolSearchSelectQueryActivatesExactTools(t *testing.T) {
	reg := New()
	reg.RegisterDeferred(AsDeferred(CategoryBrowser, stubTool{name: "playwright-screenshot"}))
	search := ToolSearchTool{Registry: reg}

	result, err := search.Execute(context.Background(), json.RawMessage(`{"action":"search","query":"select:playwright-screenshot"}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "playwright-screenshot") || !reg.Has("playwright-screenshot") {
		t.Fatalf("select result = %q, Has = %v", result.Content, reg.Has("playwright-screenshot"))
	}
}

func TestToolSearchDoesNotSuggestReadOnlyWriteTools(t *testing.T) {
	reg := NewReadOnly()
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubWriteTool{stubTool{name: "notion-create_page"}}))
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubTool{name: "notion-search"}))
	search := ToolSearchTool{Registry: reg}

	result, err := search.Execute(context.Background(), json.RawMessage(`{"action":"search","query":"notion create page search","limit":5}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "notion-create_page") {
		t.Fatalf("search result exposed write tool: %q", result.Content)
	}
	if !strings.Contains(result.Content, "notion-search") {
		t.Fatalf("search result = %q, want notion-search", result.Content)
	}
}
