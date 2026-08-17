package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type LogsTool struct {
	Base Base
}


func (t LogsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"k8s-logs",
		"",
		tool.ObjectSchema([]string{"pod"}, map[string]any{
			"pod":        map[string]any{"type": "string", "description": ""},
			"namespace":  map[string]any{"type": "string", "description": ""},
			"container":  map[string]any{"type": "string", "description": ""},
			"since":      map[string]any{"type": "string", "description": ""},
			"tail":       map[string]any{"type": "integer", "description": ""},
			"previous":   map[string]any{"type": "boolean", "description": ""},
			"grep":       map[string]any{"type": "string", "description": ""},
			"timestamps": map[string]any{"type": "boolean", "description": ""},
			"context":    map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t LogsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Pod        string `json:"pod"`
		Namespace  string `json:"namespace"`
		Container  string `json:"container"`
		Since      string `json:"since"`
		Tail       int    `json:"tail"`
		Previous   bool   `json:"previous"`
		Grep       string `json:"grep"`
		Timestamps bool   `json:"timestamps"`
		Context    string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Pod == "" {
		return tool.Result{}, fmt.Errorf("pod name or label selector is required")
	}

	// kubectl logs supports label selectors: pass -l flag when the value
	// looks like a selector (contains '=') rather than a pod name.
	var cmdArgs []string
	if strings.Contains(args.Pod, "=") {
		cmdArgs = []string{"logs", "-l", args.Pod}
	} else {
		cmdArgs = []string{"logs", args.Pod}
	}
	cmdArgs = t.Base.appendNamespace(cmdArgs, args.Namespace)
	if args.Container != "" {
		cmdArgs = append(cmdArgs, "-c", args.Container)
	}
	if args.Since != "" {
		cmdArgs = append(cmdArgs, "--since", args.Since)
	}
	tail := args.Tail
	if tail <= 0 {
		tail = 100
	}
	if tail > 2000 {
		tail = 2000
	}
	cmdArgs = append(cmdArgs, "--tail", strconv.Itoa(tail))
	if args.Previous {
		cmdArgs = append(cmdArgs, "--previous")
	}
	if args.Timestamps {
		cmdArgs = append(cmdArgs, "--timestamps")
	}

	out, err := t.Base.run(ctx, args.Context, cmdArgs)
	if err != nil {
		return tool.Result{}, err
	}

	if args.Grep != "" {
		out = grepLines(out, args.Grep)
	}
	return tool.TextResult(out), nil
}

func grepLines(text, pattern string) string {
	lines := splitLines(text)
	var matched []string
	for _, line := range lines {
		if containsFold(line, pattern) {
			matched = append(matched, line)
		}
	}
	if len(matched) == 0 {
		return "(no lines matched grep pattern)"
	}
	return joinLines(matched)
}
