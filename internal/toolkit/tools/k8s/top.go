package k8s

import (
	"context"
	"encoding/json"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type TopTool struct {
	Base Base
}

func (TopTool) Parallel() bool { return true }

func (t TopTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"k8s-top",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"resource":       map[string]any{"type": "string", "description": ""},
			"name":           map[string]any{"type": "string", "description": ""},
			"namespace":      map[string]any{"type": "string", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"sort_by":        map[string]any{"type": "string", "description": ""},
			"containers":     map[string]any{"type": "boolean", "description": ""},
		}),
	)
}

func (t TopTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Resource      string `json:"resource"`
		Name          string `json:"name"`
		Namespace     string `json:"namespace"`
		LabelSelector string `json:"label_selector"`
		SortBy        string `json:"sort_by"`
		Containers    bool   `json:"containers"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}

	resource := args.Resource
	if resource == "" {
		resource = "pods"
	}
	if resource != "pods" && resource != "nodes" && resource != "pod" && resource != "node" {
		resource = "pods"
	}

	cmdArgs := []string{"top", resource}
	if args.Name != "" {
		cmdArgs = append(cmdArgs, args.Name)
	}
	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}
	if args.LabelSelector != "" {
		cmdArgs = append(cmdArgs, "-l", args.LabelSelector)
	}
	if args.SortBy != "" {
		cmdArgs = append(cmdArgs, "--sort-by", args.SortBy)
	}
	if args.Containers && (resource == "pods" || resource == "pod") {
		cmdArgs = append(cmdArgs, "--containers")
	}

	out, err := t.Base.run(ctx, cmdArgs)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
