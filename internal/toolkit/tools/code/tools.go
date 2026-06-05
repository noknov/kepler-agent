package code

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type ReadFileTool struct {
	Paths safety.WorkspacePolicy
}

func (ReadFileTool) Parallel() bool { return true }

func (t ReadFileTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-read_file",
		"Read a text file from an allowed workspace root. Use start_line and max_lines for very large files.",
		registry.ObjectSchema([]string{"path"}, map[string]any{
			"path":       map[string]any{"type": "string", "description": "File path under one of WORKSPACE_ROOTS."},
			"start_line": map[string]any{"type": "integer", "description": "1-based start line. Defaults to 1."},
			"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines to return. Defaults to 240, max 1000."},
		}),
	)
}

func (t ReadFileTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		MaxLines  int    `json:"max_lines"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	path, err := t.Paths.ResolveReadableFile(args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	if args.StartLine <= 0 {
		args.StartLine = 1
	}
	if args.MaxLines <= 0 {
		args.MaxLines = 240
	}
	if args.MaxLines > 1000 {
		args.MaxLines = 1000
	}

	file, err := os.Open(path)
	if err != nil {
		return registry.Result{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	lineNo := 0
	emitted := 0
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return registry.Result{}, ctx.Err()
		default:
		}
		lineNo++
		if lineNo < args.StartLine {
			continue
		}
		if emitted >= args.MaxLines {
			break
		}
		b.WriteString(fmt.Sprintf("%6d  %s\n", lineNo, scanner.Text()))
		emitted++
		if b.Len() > 200_000 {
			b.WriteString("...[truncated]\n")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: b.String()}, nil
}

type SearchTool struct {
	Paths safety.WorkspacePolicy
}

func (SearchTool) Parallel() bool { return true }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-search",
		"Search code with ripgrep under an allowed workspace root. Prefer this before reading files when locating symbols, errors, routes, or config keys.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": "Literal or regex query for rg."},
			"path":  map[string]any{"type": "string", "description": "Directory or file under WORKSPACE_ROOTS. Defaults to first root."},
			"glob":  map[string]any{"type": "string", "description": "Optional rg glob, e.g. '*.go' or '!node_modules'."},
			"limit": map[string]any{"type": "integer", "description": "Maximum matching lines. Defaults to 50, max 200."},
		}),
	)
}

func (t SearchTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
		Glob  string `json:"glob"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if strings.TrimSpace(args.Query) == "" {
		return registry.Result{}, fmt.Errorf("query is required")
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	if args.Limit > 200 {
		args.Limit = 200
	}
	root := args.Path
	if root == "" && len(t.Paths.Roots) > 0 {
		root = t.Paths.Roots[0]
	}
	path, err := t.Paths.Resolve(root)
	if err != nil {
		return registry.Result{}, err
	}

	cmdArgs := []string{"--line-number", "--no-heading", "--color=never"}
	if args.Glob != "" {
		cmdArgs = append(cmdArgs, "--glob", args.Glob)
	}
	cmdArgs = append(cmdArgs, "--", args.Query, path)
	cmd := exec.CommandContext(ctx, "rg", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return registry.Result{Content: "no matches"}, nil
		}
		return registry.Result{}, fmt.Errorf("rg failed: %s", strings.TrimSpace(stderr.String()))
	}

	output := stdout.String()
	// Strip workspace root prefix from rg output so the model never sees absolute paths
	for _, r := range t.Paths.Roots {
		output = strings.ReplaceAll(output, r+"/", "")
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > args.Limit {
		lines = append(lines[:args.Limit], "...[truncated after "+strconv.Itoa(args.Limit)+" matches]")
	}
	return registry.Result{Content: strings.Join(lines, "\n")}, nil
}
