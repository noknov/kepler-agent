package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/prompts"
	"github.com/noknov/kepler-agent/packages/userprefs"
)

type LoadTool struct {
	UserPrefs userprefs.Store
}

func (LoadTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"skills-load",
		"Load the full instructions for an available skill by name. Use this before following a skill listed in the system prompt or user skills list.",
		tool.ObjectSchema([]string{"name"}, map[string]any{
			"name": map[string]any{"type": "string", "description": "Skill name exactly as shown in the available skills list."},
		}),
	)
}

func (t LoadTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if skill, ok := userprefs.LoadSkill(ctx, t.UserPrefs, call.Scope.UserID, args.Name); ok {
		content := "# " + skill.Name + "\n"
		if skill.Description != "" {
			content += "\nDescription: " + skill.Description + "\n"
		}
		content += "\nSource: Slack user upload"
		if skill.SourceFileID != "" {
			content += " " + skill.SourceFileID
		}
		content += "\n\n" + skill.Content
		return tool.TextResult(content), nil
	}
	skill, ok := prompts.LoadSkill(args.Name)
	if !ok {
		return tool.Result{}, fmt.Errorf("unknown skill %q", args.Name)
	}
	content := "# " + skill.Name + "\n"
	if skill.Description != "" {
		content += "\nDescription: " + skill.Description + "\n"
	}
	content += "\nSource: " + skill.Source + "\n\n" + skill.Content
	return tool.TextResult(content), nil
}
