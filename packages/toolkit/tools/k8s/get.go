package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

// blockedResources lists resource types that should never be retrieved because
// they contain secrets or sensitive credentials.
var blockedResources = map[string]bool{
	"secret":  true,
	"secrets": true,
}

// GetTool is a general-purpose `kubectl get` wrapper that works for any
// workload or infrastructure resource type (deployment, service, ingress,
// configmap, hpa, job, cronjob, statefulset, daemonset, node, pv, pvc, …).
// Use k8s-get_pods for pod-specific output; this covers everything else.
type GetTool struct {
	Base Base
}

func (GetTool) Parallel() bool { return true }

func (t GetTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"k8s-get",
		"",
		registry.ObjectSchema([]string{"resource"}, map[string]any{
			"resource":       map[string]any{"type": "string", "description": ""},
			"name":           map[string]any{"type": "string", "description": ""},
			"namespace":      map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"field_selector": map[string]any{"type": "string", "description": ""},
			"output":         map[string]any{"type": "string", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t GetTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
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
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Resource == "" {
		return registry.Result{}, fmt.Errorf("resource type is required (e.g. deployment, service, ingress, hpa, job, node)")
	}

	resource := strings.ToLower(strings.TrimSpace(args.Resource))
	if blockedResources[resource] {
		return registry.Result{}, fmt.Errorf("resource type %q is blocked for security reasons", args.Resource)
	}

	cmdArgs := []string{"get", resource}
	if args.Name != "" {
		cmdArgs = append(cmdArgs, args.Name)
	}
	if args.AllNamespaces {
		cmdArgs = append(cmdArgs, "--all-namespaces")
	} else {
		cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)
	}
	if args.LabelSelector != "" {
		cmdArgs = append(cmdArgs, "-l", args.LabelSelector)
	}
	if args.FieldSelector != "" {
		cmdArgs = append(cmdArgs, "--field-selector", args.FieldSelector)
	}

	// Output format: wide (default), yaml, json, name, jsonpath=...
	output := args.Output
	if output == "" {
		output = "wide"
	}
	// Validate output format to avoid injection.
	switch {
	case output == "wide", output == "yaml", output == "json", output == "name",
		strings.HasPrefix(output, "jsonpath="),
		strings.HasPrefix(output, "go-template="):
		cmdArgs = append(cmdArgs, "-o", output)
	default:
		return registry.Result{}, fmt.Errorf("unsupported output format %q; use wide, yaml, json, name, or jsonpath=...", output)
	}

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
