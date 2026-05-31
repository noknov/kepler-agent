package delegation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Tool struct {
	Manager *Manager
}

func (t Tool) Spec() llm.ToolSpec {
	profiles := "[]"
	if t.Manager != nil {
		profiles = t.Manager.ProfilesJSON()
	}
	return registry.FunctionSpec(
		"delegate-run",
		"Run a focused delegate for bounded analysis without tools. Output is unverified inference from the context you pass; corroborate with code-search, code-read_file, git-log, or gcp-logs before stating facts. Profiles: "+profiles+".",
		registry.ObjectSchema([]string{"profile", "task", "context"}, map[string]any{
			"profile": map[string]any{"type": "string", "description": "Delegate profile name, e.g. code or incident."},
			"task":    map[string]any{"type": "string", "description": "Specific bounded task."},
			"context": map[string]any{"type": "string", "description": "Compact context for the delegate. Do not paste huge raw logs."},
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
