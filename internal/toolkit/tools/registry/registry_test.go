package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/noknov/slack-copilot-agent/internal/prompts"
)

func TestToolSearchCategoriesSchemaIncludesInfrastructure(t *testing.T) {
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
	for _, got := range rawEnum {
		if got == CategoryInfrastructure {
			return
		}
	}
	t.Fatalf("categories enum = %#v, want %q", rawEnum, CategoryInfrastructure)
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
