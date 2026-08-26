package k8s

import (
	"context"
	"encoding/json"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type TopTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t TopTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-top",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"resource":       map[string]any{"type": "string", "description": ""},
			"name":           map[string]any{"type": "string", "description": ""},
			"namespace":      map[string]any{"type": "string", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"sort_by":        map[string]any{"type": "string", "description": ""},
			"containers":     map[string]any{"type": "boolean", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t TopTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Resource      string `json:"resource"`
		Name          string `json:"name"`
		Namespace     string `json:"namespace"`
		LabelSelector string `json:"label_selector"`
		SortBy        string `json:"sort_by"`
		Containers    bool   `json:"containers"`
		Context       string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	resource := args.Resource
	if resource == "" {
		resource = "pods"
	}
	if resource != "pods" && resource != "nodes" && resource != "pod" && resource != "node" {
		resource = "pods"
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
	data, err := client.metricsTop(ctx, target, resource, args.Name, args.LabelSelector)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := formatMetricsTable(data, resource)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}

func (t TopTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}
