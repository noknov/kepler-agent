package localexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

const maxOutputBytes = 100000

type CommandTool struct {
	WorkspaceRoots []string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
}

func (CommandTool) IsWrite() bool { return true }

func (t CommandTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"local-command",
		"Run a local command in an isolated coding workspace. Use this for build/test/debug commands such as go test, npm test, pytest, cargo test, and git diff. It never invokes a shell; pass argv as an array.",
		registry.ObjectSchema([]string{"argv"}, map[string]any{
			"argv": map[string]any{
				"type":        "array",
				"description": "Command argv, for example [\"go\", \"test\", \"./...\"] or [\"npm\", \"test\"].",
				"items":       map[string]any{"type": "string"},
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Optional workspace-relative or absolute working directory. Defaults to the first coding workspace root.",
			},
		}),
	)
}

func (t CommandTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Argv    []string `json:"argv"`
		Workdir string   `json:"workdir"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if len(args.Argv) == 0 || strings.TrimSpace(args.Argv[0]) == "" {
		return registry.Result{}, fmt.Errorf("argv is required")
	}
	if err := t.Guard.CheckArgv(args.Argv); err != nil {
		return registry.Result{}, err
	}
	dir, err := t.resolveWorkdir(args.Workdir)
	if err != nil {
		return registry.Result{}, err
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args.Argv[0], args.Argv[1:]...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	text := strings.TrimRight(out.String(), "\n")
	if len(text) > maxOutputBytes {
		text = text[:maxOutputBytes] + "\n...[truncated]"
	}
	if text == "" {
		text = "(command completed with no output)"
	}
	if err != nil {
		return registry.Result{Content: text}, fmt.Errorf("command failed: %w", err)
	}
	return registry.Result{Content: text}, nil
}

func (t CommandTool) resolveWorkdir(raw string) (string, error) {
	if len(t.WorkspaceRoots) == 0 {
		return "", fmt.Errorf("workspace root is required")
	}
	if strings.TrimSpace(raw) == "" {
		return filepath.Clean(t.WorkspaceRoots[0]), nil
	}
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(filepath.Clean(t.WorkspaceRoots[0]), clean)
	}
	for _, root := range t.WorkspaceRoots {
		root = filepath.Clean(root)
		rel, err := filepath.Rel(root, clean)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return clean, nil
		}
	}
	return "", fmt.Errorf("workdir is outside workspace roots")
}
