package k8s

import (
	"context"
	"encoding/json"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

// ContextsTool lists GKE clusters available to the connected Google account.
type ContextsTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (ContextsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-list_contexts",
		"List available GKE cluster contexts. Pass a returned gke_PROJECT_LOCATION_CLUSTER name as the context argument to another k8s tool.",
		tool.ObjectSchema(nil, map[string]any{}),
		tool.ReadNetworkParallel()...,
	)
}

func (t ContextsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var ignored map[string]any
	if len(call.Arguments) > 0 && string(call.Arguments) != "null" {
		if err := json.Unmarshal(call.Arguments, &ignored); err != nil {
			return tool.Result{}, err
		}
	}
	client, pending, err := begin(ctx, t.Source, call)
	if pending != nil {
		return *pending, nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	project := t.Defaults.Project
	if project == "" {
		return tool.Result{}, errClusterRequired
	}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := client.listGKEClusters(ctx, project, t.Defaults.Region)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := formatClusterContexts(data, project, t.Defaults.Region)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}

func (t ContextsTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}
