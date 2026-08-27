package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type DescribeTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t DescribeTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-describe",
		"",
		tool.ObjectSchema([]string{"resource"}, map[string]any{
			"resource":  map[string]any{"type": "string", "description": ""},
			"name":      map[string]any{"type": "string", "description": ""},
			"namespace": map[string]any{"type": "string", "description": ""},
			"context":   map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t DescribeTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Resource  string `json:"resource"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Context   string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Resource == "" {
		return tool.Result{}, fmt.Errorf("resource type is required (e.g. pod, deployment, service, node)")
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

	path, err := buildGetPath(args.Resource, target.Namespace, args.Name, false)
	if err != nil {
		return tool.Result{}, err
	}
	resourceData, err := client.getResource(ctx, target, path, nil)
	if err != nil {
		return tool.Result{}, err
	}
	var eventsData []byte
	if args.Name != "" {
		selectors := []string{"involvedObject.name=" + args.Name}
		eventsData, err = client.listEvents(ctx, target, false, selectors)
		if err != nil {
			eventsData = []byte(err.Error())
		}
	}
	out := formatDescribe(args.Resource, resourceData, eventsData)
	return tool.TextResult(out), nil
}

func (t DescribeTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}

type EventsTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t EventsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-events",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"namespace":      map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
			"for_object":     map[string]any{"type": "string", "description": ""},
			"reason":         map[string]any{"type": "string", "description": ""},
			"type":           map[string]any{"type": "string", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t EventsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Namespace     string `json:"namespace"`
		AllNamespaces bool   `json:"all_namespaces"`
		ForObject     string `json:"for_object"`
		Reason        string `json:"reason"`
		Type          string `json:"type"`
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
	selectors := []string{}
	if args.ForObject != "" {
		selectors = append(selectors, "involvedObject.name="+args.ForObject)
	}
	if args.Reason != "" {
		selectors = append(selectors, "reason="+args.Reason)
	}
	if args.Type != "" {
		selectors = append(selectors, "type="+args.Type)
	}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := client.listEvents(ctx, target, args.AllNamespaces, selectors)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

func (t EventsTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}

func joinComma(ss []string) string {
	return strings.Join(ss, ",")
}
