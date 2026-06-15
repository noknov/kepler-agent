package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type LoadTool struct{}

func (LoadTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"skills-load",
		"",
		registry.ObjectSchema([]string{"name"}, map[string]any{
			"name": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (LoadTool) Execute(_ context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	skill, ok := prompts.LoadSkill(args.Name)
	if !ok {
		return registry.Result{}, fmt.Errorf("unknown skill %q", args.Name)
	}
	content := "# " + skill.Name + "\n"
	if skill.Description != "" {
		content += "\nDescription: " + skill.Description + "\n"
	}
	content += "\nSource: " + skill.Source + "\n\n" + skill.Content
	return registry.Result{Content: content}, nil
}
