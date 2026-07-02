package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type LogsTool struct {
	Base Base
}

func (LogsTool) Parallel() bool { return true }

func (t LogsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"k8s-logs",
		"",
		registry.ObjectSchema([]string{"pod"}, map[string]any{
			"pod":        map[string]any{"type": "string", "description": ""},
			"namespace":  map[string]any{"type": "string", "description": ""},
			"container":  map[string]any{"type": "string", "description": ""},
			"since":      map[string]any{"type": "string", "description": ""},
			"tail":       map[string]any{"type": "integer", "description": ""},
			"previous":   map[string]any{"type": "boolean", "description": ""},
			"grep":       map[string]any{"type": "string", "description": ""},
			"timestamps": map[string]any{"type": "boolean", "description": ""},
		}),
	)
}

func (t LogsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Pod        string `json:"pod"`
		Namespace  string `json:"namespace"`
		Container  string `json:"container"`
		Since      string `json:"since"`
		Tail       int    `json:"tail"`
		Previous   bool   `json:"previous"`
		Grep       string `json:"grep"`
		Timestamps bool   `json:"timestamps"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Pod == "" {
		return registry.Result{}, fmt.Errorf("pod name or label selector is required")
	}

	cmdArgs := []string{"logs", args.Pod}
	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}
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

	out, err := t.Base.run(ctx, cmdArgs)
	if err != nil {
		return registry.Result{}, err
	}

	if args.Grep != "" {
		out = grepLines(out, args.Grep)
	}
	return registry.Result{Content: out}, nil
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
