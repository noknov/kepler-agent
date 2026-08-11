package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type LoadTool struct {
	UserPrefs userprefs.Store
}

func (LoadTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"skills-load",
		"Load the full instructions for an available skill by name. Use this before following a skill listed in the system prompt or user skills list.",
		registry.ObjectSchema([]string{"name"}, map[string]any{
			"name": map[string]any{"type": "string", "description": "Skill name exactly as shown in the available skills list."},
		}),
	)
}

func (t LoadTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if skill, ok := userprefs.LoadSkill(ctx, t.UserPrefs, rt.UserID, args.Name); ok {
		content := "# " + skill.Name + "\n"
		if skill.Description != "" {
			content += "\nDescription: " + skill.Description + "\n"
		}
		content += "\nSource: Slack user upload"
		if skill.SourceFileID != "" {
			content += " " + skill.SourceFileID
		}
		content += "\n\n" + skill.Content
		return registry.Result{Content: content}, nil
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
