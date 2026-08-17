package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

// RolloutTool wraps `kubectl rollout status` and `kubectl rollout history`.
// Use it to check whether a deployment is progressing/complete or to audit
// the revision history of a workload.
type RolloutTool struct {
	Base Base
}


func (t RolloutTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-rollout",
		"",
		tool.ObjectSchema([]string{"name"}, map[string]any{
			"name":      map[string]any{"type": "string", "description": ""},
			"namespace": map[string]any{"type": "string", "description": ""},
			"kind":      map[string]any{"type": "string", "description": ""},
			"action":    map[string]any{"type": "string", "description": ""},
			"revision":  map[string]any{"type": "integer", "description": ""},
			"context":   map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t RolloutTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Kind      string `json:"kind"`
		Action    string `json:"action"`
		Revision  int    `json:"revision"`
		Context   string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Name == "" {
		return tool.Result{}, fmt.Errorf("name is required (deployment or statefulset name)")
	}

	kind := args.Kind
	if kind == "" {
		kind = "deployment"
	}
	switch kind {
	case "deployment", "deploy", "statefulset", "sts", "daemonset", "ds":
	default:
		return tool.Result{}, fmt.Errorf("unsupported kind %q; use deployment, statefulset, or daemonset", kind)
	}

	action := args.Action
	if action == "" {
		action = "status"
	}
	switch action {
	case "status", "history":
	default:
		return tool.Result{}, fmt.Errorf("unsupported action %q; use status or history", action)
	}

	target := kind + "/" + args.Name
	cmdArgs := []string{"rollout", action, target}
	cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)

	// For `rollout history`, optionally show a specific revision's details.
	if action == "history" && args.Revision > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--revision=%d", args.Revision))
	}

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}
