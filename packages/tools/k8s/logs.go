package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type LogsTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
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
	client, pending, err := begin(ctx, t.Source, call)
	if pending != nil {
		return *pending, nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	target, err := resolveClusterTarget(args.Context, t.Defaults, args.Namespace)
	if err != nil {
		return tool.Result{}, err
	}
	tail := args.Tail
	if tail <= 0 {
		tail = 100
	}
	if tail > 2000 {
		tail = 2000
	}
	opts := podLogOptions{
		container:  args.Container,
		tail:       tail,
		since:      args.Since,
		previous:   args.Previous,
		timestamps: args.Timestamps,
	}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var out string
	if strings.Contains(args.Pod, "=") {
		out, err = client.podLogsBySelector(ctx, target, args.Pod, opts)
	} else {
		out, err = client.podLogs(ctx, target, args.Pod, args.Container, tail, args.Since, args.Previous, args.Timestamps)
	}
	if err != nil {
		return tool.Result{}, err
	}
	if args.Grep != "" {
		out = grepLines(out, args.Grep)
	}
	return tool.TextResult(out), nil
}

func (t LogsTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
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
