package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type DescribeTool struct {
	Base Base
}

func (DescribeTool) Parallel() bool { return true }

func (t DescribeTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"k8s-describe",
		"",
		registry.ObjectSchema([]string{"resource"}, map[string]any{
			"resource":  map[string]any{"type": "string", "description": ""},
			"name":      map[string]any{"type": "string", "description": ""},
			"namespace": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t DescribeTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Resource  string `json:"resource"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Resource == "" {
		return registry.Result{}, fmt.Errorf("resource type is required (e.g. pod, deployment, service, node)")
	}

	cmdArgs := []string{"describe", args.Resource}
	if args.Name != "" {
		cmdArgs = append(cmdArgs, args.Name)
	}
	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	out, err := t.Base.run(ctx, cmdArgs)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
