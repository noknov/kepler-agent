package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/llm"
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

type stubParallelWriteTool struct {
	stubWriteTool
}

func (stubParallelWriteTool) Parallel() bool { return true }

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

func TestCloneIsolatesDeferredActivation(t *testing.T) {
	reg := New()
	reg.Register(stubTool{name: "active-tool"})
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubTool{name: "demo-inspect"}))
	reg.Register(ToolSearchTool{Registry: reg})

	clone := reg.Clone()
	search := ToolSearchTool{Registry: clone}
	result, err := search.Execute(context.Background(), json.RawMessage(`{"action":"activate","tool_names":["demo-inspect"]}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "demo-inspect") {
		t.Fatalf("activation result = %q, want demo-inspect", result.Content)
	}
	if !clone.Has("demo-inspect") {
		t.Fatal("clone should activate demo-inspect")
	}
	if reg.Has("demo-inspect") {
		t.Fatal("base registry should keep demo-inspect deferred")
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

func TestWriteToolsNeverRunInParallel(t *testing.T) {
	reg := NewReadOnlyWithAllowedWrites("approved-write-tool")
	reg.Register(stubParallelWriteTool{stubWriteTool{stubTool{name: "approved-write-tool"}}})

	if reg.CanRunInParallel("approved-write-tool") {
		t.Fatal("write tools should not run in parallel even when they declare Parallel")
	}
}

func TestReadOnlyRegistryAllowsExplicitWriteTools(t *testing.T) {
	reg := NewReadOnlyWithAllowedWrites("approved-write-tool")
	reg.Register(stubWriteTool{stubTool{name: "approved-write-tool"}})
	reg.Register(stubWriteTool{stubTool{name: "blocked-write-tool"}})
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubWriteTool{stubTool{name: "approved-deferred-write-tool"}}))
	reg.policy.AllowedWriteTools["approved-deferred-write-tool"] = true

	names := reg.Names()
	if len(names) != 1 || names[0] != "approved-write-tool" {
		t.Fatalf("Names() = %#v, want only approved-write-tool", names)
	}
	if _, err := reg.Execute(context.Background(), "approved-write-tool", nil, Runtime{}); err != nil {
		t.Fatalf("approved write execution failed: %v", err)
	}
	if _, err := reg.Execute(context.Background(), "blocked-write-tool", nil, Runtime{}); err == nil {
		t.Fatal("blocked write execution should fail")
	}
	if !reg.ActivateTool("approved-deferred-write-tool") {
		t.Fatal("approved deferred write tool should activate")
	}
}

func TestToolSearchListAndActivate(t *testing.T) {
	reg := New()
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubTool{name: "demo-inspect"}))
	reg.RegisterDeferred(AsDeferred(CategoryIntegration, stubTool{name: "notion-search"}))
	search := ToolSearchTool{Registry: reg}

	list, err := search.Execute(context.Background(), json.RawMessage(`{"action":"list"}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Content == "" {
		t.Fatal("expected list output")
	}

	activate, err := search.Execute(context.Background(), json.RawMessage(`{"action":"activate","tool_names":["demo-inspect"]}`), Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if activate.Content == "" || !reg.Has("demo-inspect") {
		t.Fatalf("activate result = %q, Has(demo-inspect) = %v", activate.Content, reg.Has("demo-inspect"))
	}
	if reg.Has("notion-search") {
		t.Fatal("notion-search should remain deferred")
	}
}

func TestMetadataPolicyFiltersByDependencyAndSurface(t *testing.T) {
	slackWrite := WithMetadata(stubTool{name: "slack-write"}, ToolMetadata{
		Risk:         RiskExternalWrite,
		Dependencies: []string{"slack"},
		Surfaces:     []string{"slack"},
	})

	reg := NewWithPolicy(CapabilityPolicy{Surface: "coding", AvailableDeps: map[string]bool{"slack": true}})
	reg.Register(slackWrite)
	if reg.Has("slack-write") {
		t.Fatal("Has should not expose a tool for a different surface")
	}
	if _, err := reg.Execute(context.Background(), "slack-write", nil, Runtime{}); err == nil {
		t.Fatal("Execute should reject a tool for a different surface")
	}

	reg = NewWithPolicy(CapabilityPolicy{Surface: "slack", AvailableDeps: map[string]bool{"slack": false}})
	reg.Register(slackWrite)
	if len(reg.Names()) != 0 {
		t.Fatalf("Names() = %#v, want hidden missing-dependency tool", reg.Names())
	}

	reg = NewWithPolicy(CapabilityPolicy{Surface: "slack", AvailableDeps: map[string]bool{"slack": true}, AllowedWriteTools: map[string]bool{"slack-write": true}})
	reg.Register(slackWrite)
	if _, err := reg.Execute(context.Background(), "slack-write", nil, Runtime{}); err != nil {
		t.Fatalf("Execute allowed metadata write failed: %v", err)
	}
}
