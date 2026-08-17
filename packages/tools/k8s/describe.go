package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type DescribeTool struct {
	Base Base
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

	cmdArgs := []string{"describe", args.Resource}
	if args.Name != "" {
		cmdArgs = append(cmdArgs, args.Name)
	}
	cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}
