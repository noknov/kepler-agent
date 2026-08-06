package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
)

func TestToolSearchCategoriesSchemaIncludesDeferredCategories(t *testing.T) {
	spec := ToolSearchTool{}.Spec()
	properties, ok := spec.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters properties missing: %#v", spec.Function.Parameters)
	}
	categories, ok := properties["categories"].(map[string]any)
	if !ok {
		t.Fatalf("categories property missing: %#v", properties)
	}
	items, ok := categories["items"].(map[string]any)
	if !ok {
		t.Fatalf("categories items missing: %#v", categories)
	}
	rawEnum, ok := items["enum"].([]string)
	if !ok {
		t.Fatalf("categories enum has unexpected type: %#v", items["enum"])
	}
	for _, want := range []string{CategoryDiagnostics, CategoryBrowser, CategoryCode, CategoryIntegration, CategoryInfrastructure} {
		found := false
		for _, got := range rawEnum {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("categories enum = %#v, want %q", rawEnum, want)
		}
	}
}

func TestFunctionSpecAppliesNestedPromptDescriptions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tools.json"), []byte(`{
		"demo": {
			"description": "demo tool",
			"parameters": {
				"plain": "plain prompt",
				"nested": "nested prompt",
				"nested.child": "child prompt",
				"items": "items prompt",
				"items.items": "item prompt",
				"items.items.name": "item name prompt"
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prompts.LoadDirs(prompts.PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prompts.LoadDirs(prompts.PublicDir) })

	spec := FunctionSpec("demo", "", ObjectSchema(nil, map[string]any{
		"plain": map[string]any{"type": "string", "description": ""},
		"nested": map[string]any{
			"type":        "object",
			"description": "",
			"properties": map[string]any{
				"child": map[string]any{"type": "string", "description": ""},
			},
		},
		"items": map[string]any{
			"type":        "array",
			"description": "",
			"items": map[string]any{
				"type":        "object",
				"description": "",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": ""},
				},
			},
		},
	}))

	params := spec.Function.Parameters
	if got := params["properties"].(map[string]any)["plain"].(map[string]any)["description"]; got != "plain prompt" {
		t.Fatalf("plain description = %q", got)
	}
	nested := params["properties"].(map[string]any)["nested"].(map[string]any)
	if got := nested["description"]; got != "nested prompt" {
		t.Fatalf("nested description = %q", got)
	}
	if got := nested["properties"].(map[string]any)["child"].(map[string]any)["description"]; got != "child prompt" {
		t.Fatalf("nested child description = %q", got)
	}
	items := params["properties"].(map[string]any)["items"].(map[string]any)
	if got := items["description"]; got != "items prompt" {
		t.Fatalf("items description = %q", got)
	}
	item := items["items"].(map[string]any)
	if got := item["description"]; got != "item prompt" {
		t.Fatalf("item description = %q", got)
	}
	if got := item["properties"].(map[string]any)["name"].(map[string]any)["description"]; got != "item name prompt" {
		t.Fatalf("item name description = %q", got)
	}
}

func TestPolicyDecisionExplainsWriteDenial(t *testing.T) {
	reg := NewReadOnly()
	reg.Register(writeFixtureTool{})

	decision := reg.PolicyDecision("demo-write")
	if decision.Allowed {
		t.Fatalf("decision allowed write tool in read-only registry")
	}
	if decision.Risk != RiskWrite || decision.Reason == "" {
		t.Fatalf("decision = %#v, want write risk and reason", decision)
	}
	if _, err := reg.Execute(context.Background(), "demo-write", json.RawMessage(`{}`), Runtime{}); err == nil {
		t.Fatalf("Execute succeeded, want policy denial")
	}
}

type writeFixtureTool struct{}

func (writeFixtureTool) Spec() llm.ToolSpec {
	return FunctionSpec("demo-write", "demo write", ObjectSchema(nil, map[string]any{}))
}

func (writeFixtureTool) Execute(context.Context, json.RawMessage, Runtime) (Result, error) {
	return Result{Content: "ok"}, nil
}

func (writeFixtureTool) Metadata() ToolMetadata {
	return ToolMetadata{Risk: RiskWrite}
}
