package delegation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/internal/llm"
	"github.com/noknov/slack-copilot-agent/internal/prompts"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
)

type Tool struct {
	Manager *Manager
}

func (Tool) Parallel() bool { return true }

func (t Tool) CloneForRegistry(reg *registry.Registry) registry.Tool {
	t.Manager = t.Manager.WithTools(reg)
	return t
}

func (t Tool) Spec() llm.ToolSpec {
	profiles := "[]"
	if t.Manager != nil {
		profiles = t.Manager.ProfilesJSON()
	}
	return registry.FunctionSpec(
		"delegate-run",
		prompts.PromptText("delegate_profiles_prefix", "")+profiles+".",
		registry.ObjectSchema([]string{"profile", "task", "context"}, map[string]any{
			"profile": map[string]any{"type": "string", "description": ""},
			"task":    map[string]any{"type": "string", "description": ""},
			"context": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t Tool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	if t.Manager == nil {
		return registry.Result{}, fmt.Errorf("delegation manager is not configured")
	}
	var args struct {
		Profile string `json:"profile"`
		Task    string `json:"task"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	out, err := t.Manager.Run(ctx, args.Profile, args.Task, args.Context)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
