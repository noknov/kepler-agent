package k8s

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

// ContextsTool lists pre-provisioned kubeconfig contexts without exposing
// credentials. Callers then pass one of these names to a k8s-* tool's context
// argument; no shared default context is mutated.
type ContextsTool struct{ Base Base }

func (ContextsTool) Parallel() bool { return true }

func (ContextsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("k8s-list_contexts", "List available Kubernetes contexts. Pass a returned name as the context argument to another k8s tool; this does not change the process-wide kubeconfig default.", registry.ObjectSchema(nil, map[string]any{}))
}

func (t ContextsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	// Decode so malformed tool arguments fail consistently with other tools.
	var ignored map[string]any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &ignored); err != nil {
			return registry.Result{}, err
		}
	}
	out, err := t.Base.run(ctx, "", []string{"config", "get-contexts", "-o", "name"})
	if err != nil {
		return registry.Result{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "no Kubernetes contexts are configured"
	}
	return registry.Result{Content: out}, nil
}
