package k8s

import (
	"context"
	"encoding/json"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

// EventsTool runs `kubectl get events` sorted by last-seen timestamp.
// Kubernetes events are the first place to look when a pod is CrashLooping,
// failing to schedule, or stuck in Pending — this tool surfaces them directly.
type EventsTool struct {
	Base Base
}


func (t EventsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-events",
		"",
		tool.ObjectSchema(nil, map[string]any{
			"namespace":      map[string]any{"type": "string", "description": ""},
			"all_namespaces": map[string]any{"type": "boolean", "description": ""},
			"for_object":     map[string]any{"type": "string", "description": ""},
			"reason":         map[string]any{"type": "string", "description": ""},
			"type":           map[string]any{"type": "string", "description": ""},
			"context":        map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t EventsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Namespace     string `json:"namespace"`
		AllNamespaces bool   `json:"all_namespaces"`
		ForObject     string `json:"for_object"`
		Reason        string `json:"reason"`
		Type          string `json:"type"`
		Context       string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}

	cmdArgs := []string{"get", "events", "--sort-by=.lastTimestamp"}
	if args.AllNamespaces {
		cmdArgs = append(cmdArgs, "--all-namespaces")
	} else {
		cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)
	}

	// --field-selector filters: involvedObject.name=<pod>, reason=<reason>, type=Warning|Normal
	selectors := []string{}
	if args.ForObject != "" {
		selectors = append(selectors, "involvedObject.name="+args.ForObject)
	}
	if args.Reason != "" {
		selectors = append(selectors, "reason="+args.Reason)
	}
	if args.Type != "" {
		selectors = append(selectors, "type="+args.Type)
	}
	if len(selectors) > 0 {
		cmdArgs = append(cmdArgs, "--field-selector="+joinComma(selectors))
	}

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(out), nil
}

func joinComma(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ","
		}
		result += s
	}
	return result
}
