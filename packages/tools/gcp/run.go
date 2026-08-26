package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type RunServicesTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t RunServicesTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"gcp-run_services",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"action":  map[string]any{"type": "string", "description": ""},
			"service": map[string]any{"type": "string", "description": ""},
			"project": map[string]any{"type": "string", "description": ""},
			"region":  map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t RunServicesTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Action  string `json:"action"`
		Service string `json:"service"`
		Project string `json:"project"`
		Region  string `json:"region"`
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
	region := client.region(args.Region)
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var data []byte
	switch action {
	case "list":
		data, err = client.listRunServices(ctx, project, region)
	case "describe":
		data, err = client.describeRunService(ctx, project, region, args.Service)
	}
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

func (t RunServicesTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}

type RunRevisionsTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t RunRevisionsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"gcp-run_revisions",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"service": map[string]any{"type": "string", "description": ""},
			"project": map[string]any{"type": "string", "description": ""},
			"region":  map[string]any{"type": "string", "description": ""},
			"limit":   map[string]any{"type": "integer", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t RunRevisionsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Service string `json:"service"`
		Project string `json:"project"`
		Region  string `json:"region"`
		Limit   int    `json:"limit"`
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
	project, err := client.projectID(args.Project)
	if err != nil {
		return tool.Result{}, err
	}
	region := client.region(args.Region)
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := client.listRunRevisions(ctx, project, region, args.Service, args.Limit)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

func (t RunRevisionsTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}
