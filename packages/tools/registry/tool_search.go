package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
)

var categoryDescriptions = map[string]string{
	CategoryDiagnostics:    "Incident investigation helpers: incident briefs, timelines, and evidence boards.",
	CategoryCode:           "Advanced code intelligence: static package graphs, symbols, definitions, references, callers, callees, callgraphs, and impact analysis.",
	CategoryIntegration:    "External integrations: GitHub, Notion, YouTrack, Slack Canvas, Luckin MCP, TTS, and related APIs.",
	CategoryInfrastructure: "Infrastructure and operations tools: Kubernetes, GCP Cloud Logging, Cloud Run, clusters, pods, logs, events, and rollouts.",
}

// ToolSearchTool is an explicit capability catalog. Deferred tools are listed
// by stable category and activated only by their exact name or category; it
// intentionally does not rank natural-language queries heuristically.
type ToolSearchTool struct {
	Registry *Registry
}

func (t ToolSearchTool) CloneForRegistry(reg *Registry) Tool {
	t.Registry = reg
	return t
}

func (ToolSearchTool) Spec() llm.ToolSpec {
	return FunctionSpec(
		"tool_search",
		"List deferred capability categories and activate tools explicitly. Call action=list to inspect available categories and exact tool names, then call action=activate with tool_names or categories. Activation changes the tool schemas available on subsequent steps.",
		ObjectSchema([]string{"action"}, map[string]any{
			"action": map[string]any{
				"type": "string", "enum": []string{"list", "activate"},
				"description": "list: show deferred categories and exact tool names. activate: load named tools or complete categories.",
			},
			"categories": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "enum": []string{CategoryDiagnostics, CategoryCode, CategoryIntegration, CategoryInfrastructure}},
				"description": "Deferred categories to activate.",
			},
			"tool_names": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "Exact deferred tool names to activate.",
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
		ToolNames  []string `json:"tool_names"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	switch strings.TrimSpace(args.Action) {
	case "list":
		return Result{Content: t.listCategories()}, nil
	case "activate":
		return Result{Content: t.activate(args.Categories, args.ToolNames)}, nil
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
	b.WriteString("Deferred capability catalog (use action=activate with exact tool_names or categories):\n")
	for _, category := range categories {
		description := categoryDescriptions[category]
		if description == "" {
			description = "Additional tools for specialized workflows."
		}
		tools := t.Registry.DeferredToolNames(category)
		b.WriteString(fmt.Sprintf("\n- %s (%d tools): %s\n", category, len(tools), description))
		for _, name := range tools {
			b.WriteString("  - ")
			b.WriteString(name)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func (t ToolSearchTool) activate(categories []string, toolNames []string) string {
	activated := make([]string, 0, len(toolNames))
	unknown := make([]string, 0)
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" || t.Registry.Has(name) {
			continue
		}
		if t.Registry.ActivateTool(name) {
			activated = append(activated, name)
		} else {
			unknown = append(unknown, name)
		}
	}
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		if len(t.Registry.DeferredToolNames(category)) == 0 && !t.Registry.hasCategory(category) {
			unknown = append(unknown, category)
			continue
		}
		activated = append(activated, t.Registry.ActivateCategory(category)...)
	}
	sort.Strings(activated)
	sort.Strings(unknown)
	var b strings.Builder
	if len(activated) > 0 {
		b.WriteString(fmt.Sprintf("Activated %d tools:\n", len(activated)))
		for _, name := range activated {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteByte('\n')
		}
		b.WriteString("\nThese tools are available on subsequent steps.")
	}
	if len(unknown) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Unknown or unavailable tools/categories: ")
		b.WriteString(strings.Join(unknown, ", "))
	}
	if b.Len() == 0 {
		return "No tools activated. Requested tools/categories are already active, or no tools/categories were requested."
	}
	return strings.TrimSpace(b.String())
}

func (r *Registry) hasCategory(category string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.categories[category]
	return ok
}
