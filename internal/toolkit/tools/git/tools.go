package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type Base struct {
	Paths   safety.WorkspacePolicy
	Guard   safety.CommandPolicy
	Timeout time.Duration
}

type StatusTool struct{ Base }

func (t StatusTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git.status",
		"Read git branch and working tree status. This is read-only.",
		registry.ObjectSchema(nil, map[string]any{
			"repo": map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
		}),
	)
}

func (t StatusTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo string `json:"repo"`
	}
	_ = json.Unmarshal(raw, &args)
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.run(ctx, repo, "status", "--short", "--branch")
	return registry.Result{Content: out}, err
}

type LogTool struct{ Base }

func (t LogTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git.log",
		"Read recent commits from git log. This is read-only.",
		registry.ObjectSchema(nil, map[string]any{
			"repo":  map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"limit": map[string]any{"type": "integer", "description": "Number of commits. Defaults to 10, max 50."},
		}),
	)
}

func (t LogTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo  string `json:"repo"`
		Limit int    `json:"limit"`
	}
	_ = json.Unmarshal(raw, &args)
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.run(ctx, repo, "log", "--oneline", "-n", strconv.Itoa(args.Limit))
	return registry.Result{Content: out}, err
}

type ShowTool struct{ Base }

func (t ShowTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git.show",
		"Read a commit diff or file at revision. This is read-only and output is capped.",
		registry.ObjectSchema([]string{"rev"}, map[string]any{
			"repo":      map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"rev":       map[string]any{"type": "string", "description": "Commit SHA, branch, tag, or ref."},
			"path":      map[string]any{"type": "string", "description": "Optional file path inside repo."},
			"max_chars": map[string]any{"type": "integer", "description": "Maximum characters. Defaults to 12000, max 50000."},
		}),
	)
}

func (t ShowTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo     string `json:"repo"`
		Rev      string `json:"rev"`
		Path     string `json:"path"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.MaxChars <= 0 {
		args.MaxChars = 12000
	}
	if args.MaxChars > 50000 {
		args.MaxChars = 50000
	}
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	cmdArgs := []string{"show", "--stat", "--patch", args.Rev}
	if args.Path != "" {
		cmdArgs = append(cmdArgs, "--", args.Path)
	}
	out, err := t.run(ctx, repo, cmdArgs...)
	if len(out) > args.MaxChars {
		out = out[:args.MaxChars] + "\n...[truncated]"
	}
	return registry.Result{Content: out}, err
}

func (b Base) repo(path string) (string, error) {
	if path == "" && len(b.Paths.Roots) > 0 {
		path = b.Paths.Roots[0]
	}
	return b.Paths.Resolve(path)
}

func (b Base) run(ctx context.Context, repo string, args ...string) (string, error) {
	display := "git -C " + repo + " " + strings.Join(args, " ")
	if err := b.Guard.Check(display); err != nil {
		return "", err
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdArgs := append([]string{"-C", repo, "--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git failed: %s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
