package k8s

import (
	"context"
	"encoding/json"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type TopTool struct {
	Base Base
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

	cmdArgs := []string{"top", resource}
	if args.Name != "" {
		cmdArgs = append(cmdArgs, args.Name)
	}
	if resource != "nodes" && resource != "node" {
		cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)
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

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}
