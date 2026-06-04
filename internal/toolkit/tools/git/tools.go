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
		"git-status",
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

type FetchRefTool struct{ Base }

func (t FetchRefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-fetch_ref",
		"Fetch origin and resolve a branch to an immutable commit ref without changing the working tree. Use when the user names a branch, and use with no branch to select the default branch main, then master.",
		registry.ObjectSchema(nil, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"branch": map[string]any{"type": "string", "description": "Branch to analyze. If omitted, tries main, then master."},
		}),
	)
}

func (t FetchRefTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal(raw, &args)
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	if _, err := t.run(ctx, repo, "fetch", "--prune", "--no-write-fetch-head", "origin"); err != nil {
		return registry.Result{}, err
	}

	branch := strings.TrimSpace(args.Branch)
	if branch == "" {
		branch, err = t.defaultBranch(ctx, repo)
		if err != nil {
			return registry.Result{}, err
		}
	}
	if err := validateRefPart(branch); err != nil {
		return registry.Result{}, err
	}
	ref := "origin/" + branch
	if !t.refExists(ctx, repo, ref) {
		return registry.Result{}, fmt.Errorf("branch %q does not exist on origin", branch)
	}
	commit, err := t.run(ctx, repo, "rev-parse", ref)
	if err != nil {
		return registry.Result{}, err
	}
	short, err := t.run(ctx, repo, "rev-parse", "--short", ref)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: fmt.Sprintf("branch=%s\nbranch_ref=%s\nref=%s\ncommit=%s\nworking_tree_changed=false", branch, ref, strings.TrimSpace(commit), strings.TrimSpace(short))}, nil
}

type SearchRefTool struct{ Base }

func (t SearchRefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-search_ref",
		"Search code at a git ref without changing the working tree. Use this instead of code-search when analyzing a specified branch.",
		registry.ObjectSchema([]string{"query", "ref"}, map[string]any{
			"repo":  map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"ref":   map[string]any{"type": "string", "description": "Immutable ref returned by git-fetch_ref, usually a commit SHA."},
			"query": map[string]any{"type": "string", "description": "Regex query for git grep."},
			"path":  map[string]any{"type": "string", "description": "Optional pathspec inside repo."},
			"limit": map[string]any{"type": "integer", "description": "Maximum matching lines. Defaults to 50, max 200."},
		}),
	)
}

func (t SearchRefTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo  string `json:"repo"`
		Ref   string `json:"ref"`
		Query string `json:"query"`
		Path  string `json:"path"`
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
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	ref, err := t.resolveRef(ctx, repo, args.Ref)
	if err != nil {
		return registry.Result{}, err
	}
	path, err := cleanGitPath(args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	cmdArgs := []string{"grep", "-n", "--no-color", "-I", "-e", args.Query, ref, "--"}
	if path != "" {
		cmdArgs = append(cmdArgs, path)
	}
	out, err := t.run(ctx, repo, cmdArgs...)
	if err != nil {
		if strings.TrimSpace(out) == "" {
			return registry.Result{Content: "no matches"}, nil
		}
		return registry.Result{}, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > args.Limit {
		lines = append(lines[:args.Limit], "...[truncated after "+strconv.Itoa(args.Limit)+" matches]")
	}
	return registry.Result{Content: strings.Join(lines, "\n")}, nil
}

type ReadFileRefTool struct{ Base }

func (t ReadFileRefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-read_file_ref",
		"Read a file at a git ref without changing the working tree. Use this instead of code-read_file when analyzing a specified branch.",
		registry.ObjectSchema([]string{"ref", "path"}, map[string]any{
			"repo":       map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"ref":        map[string]any{"type": "string", "description": "Immutable ref returned by git-fetch_ref, usually a commit SHA."},
			"path":       map[string]any{"type": "string", "description": "File path inside repo."},
			"start_line": map[string]any{"type": "integer", "description": "1-based start line. Defaults to 1."},
			"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines to return. Defaults to 240, max 1000."},
		}),
	)
}

func (t ReadFileRefTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo      string `json:"repo"`
		Ref       string `json:"ref"`
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		MaxLines  int    `json:"max_lines"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	ref, err := t.resolveRef(ctx, repo, args.Ref)
	if err != nil {
		return registry.Result{}, err
	}
	path, err := cleanGitPath(args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	if path == "" {
		return registry.Result{}, fmt.Errorf("path is required")
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
	out, err := t.run(ctx, repo, "show", ref+":"+path)
	if err != nil {
		return registry.Result{}, err
	}
	lines := strings.Split(out, "\n")
	var b strings.Builder
	emitted := 0
	for idx, line := range lines {
		lineNo := idx + 1
		if lineNo < args.StartLine {
			continue
		}
		if emitted >= args.MaxLines {
			break
		}
		b.WriteString(fmt.Sprintf("%6d  %s\n", lineNo, line))
		emitted++
		if b.Len() > 200_000 {
			b.WriteString("...[truncated]\n")
			break
		}
	}
	return registry.Result{Content: b.String()}, nil
}

type LogTool struct{ Base }

func (t LogTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-log",
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
		"git-show",
		"Read a commit diff or file at revision. This is read-only and output is capped.",
		registry.ObjectSchema([]string{"rev"}, map[string]any{
			"repo":      map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"rev":       map[string]any{"type": "string", "description": "Commit SHA, branch, tag, or ref."},
			"path":      map[string]any{"type": "string", "description": "Optional file path inside repo."},
			"max_chars": map[string]any{"type": "integer", "description": "Maximum characters. Defaults to 12000, max 50000."},
		}),
	)
}

func (t ShowTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
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
	rev := strings.TrimSpace(args.Rev)
	if err := validateRefPart(rev); err != nil {
		return registry.Result{}, err
	}
	path, err := cleanGitPath(args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	cmdArgs := []string{"show", "--stat", "--patch", rev}
	if path != "" {
		cmdArgs = append(cmdArgs, "--", path)
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

func (b Base) defaultBranch(ctx context.Context, repo string) (string, error) {
	for _, branch := range []string{"main", "master"} {
		if b.refExists(ctx, repo, "origin/"+branch) || b.refExists(ctx, repo, branch) {
			return branch, nil
		}
	}
	return "", fmt.Errorf("could not find default branch main or master")
}

func (b Base) resolveRef(ctx context.Context, repo, raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		branch, err := b.defaultBranch(ctx, repo)
		if err != nil {
			return "", err
		}
		ref = "origin/" + branch
	}
	if err := validateRefPart(ref); err != nil {
		return "", err
	}
	if !b.refExists(ctx, repo, ref) {
		return "", fmt.Errorf("ref %q does not exist", ref)
	}
	return ref, nil
}

func (b Base) refExists(ctx context.Context, repo, ref string) bool {
	_, err := b.run(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

func validateRefPart(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref is required")
	}
	if strings.Contains(ref, "..") || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\n\r~^:?*[\\") {
		return fmt.Errorf("invalid ref %q", ref)
	}
	return nil
}

func cleanGitPath(path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return "", nil
	}
	if strings.Contains(path, "..") || strings.HasPrefix(path, "-") || strings.ContainsAny(path, "\x00\n\r") {
		return "", fmt.Errorf("invalid path %q", path)
	}
	return path, nil
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
