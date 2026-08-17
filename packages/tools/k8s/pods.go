package k8s

import (
	"context"
	"encoding/json"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type GetPodsTool struct {
	Base Base
}


func (t GetPodsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-get_pods",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"namespace":      map[string]any{"type": "string", "description": ""},
			"label_selector": map[string]any{"type": "string", "description": ""},
			"field_selector": map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t GetPodsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Namespace     string `json:"namespace"`
		LabelSelector string `json:"label_selector"`
		FieldSelector string `json:"field_selector"`
		AllNamespaces bool   `json:"all_namespaces"`
		Context       string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}

	cmdArgs := []string{"get", "pods", "-o", "wide"}
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

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}
