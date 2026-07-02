package k8s

import (
	"context"
	"encoding/json"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type GetPodsTool struct {
	Base Base
}

func (GetPodsTool) Parallel() bool { return true }

func (t GetPodsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"k8s-get_pods",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"namespace":      map[string]any{"type": "string", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"field_selector": map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
		}),
	)
}

func (t GetPodsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Namespace     string `json:"namespace"`
		LabelSelector string `json:"label_selector"`
		FieldSelector string `json:"field_selector"`
		AllNamespaces bool   `json:"all_namespaces"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}

	cmdArgs := []string{"get", "pods", "-o", "wide"}
	if args.AllNamespaces {
		cmdArgs = append(cmdArgs, "--all-namespaces")
	} else if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}
	if args.LabelSelector != "" {
		cmdArgs = append(cmdArgs, "-l", args.LabelSelector)
	}
	if args.FieldSelector != "" {
		cmdArgs = append(cmdArgs, "--field-selector", args.FieldSelector)
	}

	out, err := t.Base.run(ctx, cmdArgs)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
