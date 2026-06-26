package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

const cacheKey = "agent-plan"

type PlanTool struct{}

type Item struct {
	ID     string `json:"id,omitempty"`
	Task   string `json:"task"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

func (PlanTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"plan-update",
		"Create or replace the current execution plan for a complex multi-step agent task. Use this before substantial work, and update it as steps move through pending, in_progress, completed, or blocked. Skip for trivial one-step questions.",
		registry.ObjectSchema([]string{"items"}, map[string]any{
			"items": map[string]any{
				"type":        "array",
				"description": "Ordered task list. Keep items concrete and outcome-oriented.",
				"items": registry.ObjectSchema([]string{"task", "status"}, map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Short stable identifier, such as 1, 2, or verify.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "Concrete task in imperative form.",
					},
					"status": map[string]any{
						"type":        "string",
						"enum":        []string{"pending", "in_progress", "completed", "blocked"},
						"description": "Current task state. Keep at most one item in_progress.",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "Optional blocker, verification result, or important detail.",
					},
				}),
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "Optional short reason for this plan update.",
			},
		}),
	)
}

func (PlanTool) Execute(_ context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Items   []Item `json:"items"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	items, err := normalizeItems(args.Items)
	if err != nil {
		return registry.Result{}, err
	}
	if rt.Cache != nil {
		rt.Cache.Set(cacheKey, items)
	}
	return registry.Result{Content: formatPlan(strings.TrimSpace(args.Summary), items)}, nil
}

func normalizeItems(items []Item) ([]Item, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("items must contain at least one task")
	}
	out := make([]Item, 0, len(items))
	inProgress := 0
	for i, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = fmt.Sprintf("%d", i+1)
		}
		item.Task = strings.TrimSpace(item.Task)
		if item.Task == "" {
			return nil, fmt.Errorf("items[%d].task is required", i)
		}
		item.Status = strings.TrimSpace(item.Status)
		switch item.Status {
		case "pending", "in_progress", "completed", "blocked":
		default:
			return nil, fmt.Errorf("items[%d].status must be pending, in_progress, completed, or blocked", i)
		}
		if item.Status == "in_progress" {
			inProgress++
		}
		item.Note = strings.TrimSpace(item.Note)
		out = append(out, item)
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("at most one item may be in_progress")
	}
	return out, nil
}

func formatPlan(summary string, items []Item) string {
	var b strings.Builder
	if summary != "" {
		b.WriteString("Plan update: ")
		b.WriteString(summary)
		b.WriteString("\n")
	}
	b.WriteString("Current plan:")
	for _, item := range items {
		b.WriteString("\n- ")
		b.WriteString(item.ID)
		b.WriteString(" [")
		b.WriteString(item.Status)
		b.WriteString("] ")
		b.WriteString(item.Task)
		if item.Note != "" {
			b.WriteString(" - ")
			b.WriteString(item.Note)
		}
	}
	return b.String()
}
