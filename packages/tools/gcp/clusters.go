package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type ClustersTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t ClustersTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"gcp-clusters",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"action":  map[string]any{"type": "string", "description": ""},
			"cluster": map[string]any{"type": "string", "description": ""},
			"project": map[string]any{"type": "string", "description": ""},
			"region":  map[string]any{"type": "string", "description": ""},
			"zone":    map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t ClustersTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Action  string `json:"action"`
		Cluster string `json:"cluster"`
		Project string `json:"project"`
		Region  string `json:"region"`
		Zone    string `json:"zone"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	action := args.Action
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "describe":
	default:
		return tool.Result{}, fmt.Errorf("unsupported action %q; use list or describe", action)
	}

	client, pending, err := begin(ctx, t.Source, call)
	if pending != nil {
		return *pending, nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	project, err := client.projectID(args.Project)
	if err != nil {
		return tool.Result{}, err
	}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var data []byte
	switch action {
	case "list":
		data, err = client.listClusters(ctx, project)
	case "describe":
		location := strings.TrimSpace(args.Zone)
		if location == "" {
			location = client.region(args.Region)
		}
		data, err = client.describeCluster(ctx, project, location, args.Cluster)
	}
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

func (t ClustersTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}
