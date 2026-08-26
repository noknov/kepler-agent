package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// GetTool is a general-purpose Kubernetes GET wrapper for workload and infra resources.
type GetTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

var blockedResources = map[string]bool{
	"secret":  true,
	"secrets": true,
}

func (t GetTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-get",
		"",
		tool.ObjectSchema([]string{"resource"}, map[string]any{
			"resource":       map[string]any{"type": "string", "description": ""},
			"name":           map[string]any{"type": "string", "description": ""},
			"namespace":      map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"field_selector": map[string]any{"type": "string", "description": ""},
			"output":         map[string]any{"type": "string", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t GetTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Resource      string `json:"resource"`
		Name          string `json:"name"`
		Namespace     string `json:"namespace"`
		AllNamespaces bool   `json:"all_namespaces"`
		LabelSelector string `json:"label_selector"`
		FieldSelector string `json:"field_selector"`
		Output        string `json:"output"`
		Context       string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Resource == "" {
		return tool.Result{}, fmt.Errorf("resource type is required (e.g. deployment, service, ingress, hpa, job, node)")
	}
	resource := strings.ToLower(strings.TrimSpace(args.Resource))
	if blockedResources[resource] {
		return tool.Result{}, fmt.Errorf("resource type %q is blocked for security reasons", args.Resource)
	}
	output := args.Output
	if output != "" && output != "wide" && output != "yaml" && output != "json" && output != "name" &&
		!strings.HasPrefix(output, "jsonpath=") && !strings.HasPrefix(output, "go-template=") {
		return tool.Result{}, fmt.Errorf("unsupported output format %q; use wide, yaml, json, or name", output)
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
	path, err := buildGetPath(args.Resource, target.Namespace, args.Name, args.AllNamespaces)
	if err != nil {
		return tool.Result{}, err
	}
	query := listQuery(args.LabelSelector, args.FieldSelector)
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := client.getResource(ctx, target, path, query)
	if err != nil {
		return tool.Result{}, err
	}
	if output == "wide" && (resource == "pod" || resource == "pods") {
		out, ferr := formatPodsWide(data)
		if ferr == nil {
			return tool.TextResult(out), nil
		}
	}
	return tool.TextResult(string(data)), nil
}

func (t GetTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}

func listQuery(labelSelector, fieldSelector string) url.Values {
	query := url.Values{}
	if labelSelector != "" {
		query.Set("labelSelector", labelSelector)
	}
	if fieldSelector != "" {
		query.Set("fieldSelector", fieldSelector)
	}
	return query
}
