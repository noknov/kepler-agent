package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

// RolloutTool wraps `kubectl rollout status` and `kubectl rollout history`.
// Use it to check whether a deployment is progressing/complete or to audit
// the revision history of a workload.
type RolloutTool struct {
	Base Base
}

func (RolloutTool) Parallel() bool { return true }

func (t RolloutTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"k8s-rollout",
		"",
		registry.ObjectSchema([]string{"name"}, map[string]any{
			"name":      map[string]any{"type": "string", "description": ""},
			"namespace": map[string]any{"type": "string", "description": ""},
			"kind":      map[string]any{"type": "string", "description": ""},
			"action":    map[string]any{"type": "string", "description": ""},
			"revision":  map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t RolloutTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Kind      string `json:"kind"`
		Action    string `json:"action"`
		Revision  int    `json:"revision"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Name == "" {
		return registry.Result{}, fmt.Errorf("name is required (deployment or statefulset name)")
	}

	kind := args.Kind
	if kind == "" {
		kind = "deployment"
	}
	switch kind {
	case "deployment", "deploy", "statefulset", "sts", "daemonset", "ds":
	default:
		return registry.Result{}, fmt.Errorf("unsupported kind %q; use deployment, statefulset, or daemonset", kind)
	}

	action := args.Action
	if action == "" {
		action = "status"
	}
	switch action {
	case "status", "history":
	default:
		return registry.Result{}, fmt.Errorf("unsupported action %q; use status or history", action)
	}

	target := kind + "/" + args.Name
	cmdArgs := []string{"rollout", action, target}
	cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)

	// For `rollout history`, optionally show a specific revision's details.
	if action == "history" && args.Revision > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--revision=%d", args.Revision))
	}

	out, err := t.Base.run(ctx, cmdArgs)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
