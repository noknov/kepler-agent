package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type RolloutTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
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
	switch normalizeWorkloadKind(kind) {
	case "deployment", "statefulset", "daemonset":
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
	client, pending, err := begin(ctx, t.Source, call)
	if pending != nil {
		return *pending, nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	target, err := resolveClusterTarget(args.Context, t.Defaults, args.Namespace)
	if err != nil {
		return tool.Result{}, err
	}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := client.deploymentRollout(ctx, target, args.Name, kind, action, args.Revision)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}

func (t RolloutTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}
