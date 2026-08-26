package k8s

import (
	"context"
	"encoding/json"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type GetPodsTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t GetPodsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-get_pods",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"namespace":      map[string]any{"type": "string", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"field_selector": map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t GetPodsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Namespace     string `json:"namespace"`
		LabelSelector string `json:"label_selector"`
		FieldSelector string `json:"field_selector"`
		AllNamespaces bool   `json:"all_namespaces"`
		Context       string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
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
	data, err := client.listPods(ctx, target, args.AllNamespaces, args.LabelSelector, args.FieldSelector)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := formatPodsWide(data)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}

func (t GetPodsTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}
