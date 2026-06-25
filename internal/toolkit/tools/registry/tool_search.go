package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
)

var categoryDescriptions = map[string]string{
	CategoryDiagnostics: "Incident investigation helpers: incident briefs, timelines, and evidence boards.",
	CategoryBrowser:     "Playwright browser automation: navigate, snapshot, click, type, screenshots, and page evaluation.",
	CategoryIntegration: "External integrations: Notion, YouTrack, Luckin MCP, and related APIs.",
}

type ToolSearchTool struct {
	Registry *Registry
}

func (ToolSearchTool) Spec() llm.ToolSpec {
	return FunctionSpec(
		"tool_search",
		"Discover deferred tool categories and activate them when needed. Call with action=list to see available categories, then action=activate with category names to load those tools for subsequent steps.",
		ObjectSchema([]string{"action"}, map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "activate"},
				"description": "list: show deferred categories; activate: load tools from requested categories.",
			},
			"categories": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
					"enum": []string{CategoryDiagnostics, CategoryBrowser, CategoryIntegration},
				},
				"description": "Categories to activate. Required when action=activate.",
			},
		}),
	)
}

func (t ToolSearchTool) Execute(_ context.Context, raw json.RawMessage, _ Runtime) (Result, error) {
	if t.Registry == nil {
		return Result{}, fmt.Errorf("tool registry is not configured")
	}
	var args struct {
		Action     string   `json:"action"`
		Categories []string `json:"categories"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	switch strings.TrimSpace(args.Action) {
	case "list":
		return Result{Content: t.listCategories()}, nil
	case "activate":
		return Result{Content: t.activateCategories(args.Categories)}, nil
	default:
		return Result{}, fmt.Errorf("action must be list or activate")
	}
}

func (t ToolSearchTool) listCategories() string {
	categories := t.Registry.DeferredCategories()
	if len(categories) == 0 {
		return "No deferred tool categories are available. All registered tools are already active."
	}
	var b strings.Builder
	b.WriteString("Deferred tool categories (call tool_search with action=activate to load):\n")
	for _, category := range categories {
		desc := categoryDescriptions[category]
		if desc == "" {
			desc = "Additional tools for specialized workflows."
		}
		tools := t.Registry.DeferredToolNames(category)
		b.WriteString(fmt.Sprintf("\n- %s (%d tools): %s\n", category, len(tools), desc))
		for _, name := range tools {
			b.WriteString("  - ")
			b.WriteString(name)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func (t ToolSearchTool) activateCategories(categories []string) string {
	if len(categories) == 0 {
		return "No categories requested. Call tool_search with action=list to see available categories."
	}
	var activated []string
	var unknown []string
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		if len(t.Registry.DeferredToolNames(category)) == 0 && !containsCategory(t.Registry.categories, category) {
			unknown = append(unknown, category)
			continue
		}
		activated = append(activated, t.Registry.ActivateCategory(category)...)
	}
	var b strings.Builder
	if len(activated) > 0 {
		b.WriteString(fmt.Sprintf("Activated %d tools:\n", len(activated)))
		for _, name := range activated {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteByte('\n')
		}
		b.WriteString("\nThese tools are now available on subsequent steps.")
	} else if len(unknown) > 0 {
		b.WriteString("No tools activated. Unknown or already-active categories: ")
		b.WriteString(strings.Join(unknown, ", "))
	} else {
		b.WriteString("Requested categories are already active.")
	}
	return strings.TrimSpace(b.String())
}

func containsCategory(categories map[string][]string, category string) bool {
	_, ok := categories[category]
	return ok
}
