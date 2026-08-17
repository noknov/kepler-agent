package k8s

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

// ContextsTool lists pre-provisioned kubeconfig contexts without exposing
// credentials. Callers then pass one of these names to a k8s-* tool's context
// argument; no shared default context is mutated.
type ContextsTool struct{ Base Base }


func (ContextsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("k8s-list_contexts", "List available Kubernetes contexts. Pass a returned name as the context argument to another k8s tool; this does not change the process-wide kubeconfig default.", tool.ObjectSchema(nil, map[string]any{}))
}

func (t ContextsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	// Decode so malformed tool arguments fail consistently with other tools.
	var ignored map[string]any
	if len(call.Arguments) > 0 && string(call.Arguments) != "null" {
		if err := json.Unmarshal(call.Arguments, &ignored); err != nil {
			return tool.Result{}, err
		}
	}
	out, err := t.Base.run(ctx, "", []string{"config", "get-contexts", "-o", "name"})
	if err != nil {
		return tool.Result{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "no Kubernetes contexts are configured"
	}
	return tool.TextResult(out), nil
}
