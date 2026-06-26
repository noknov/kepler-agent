package code

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
		"",
		registry.ObjectSchema([]string{"path"}, map[string]any{
			"path":       map[string]any{"type": "string", "description": ""},
			"start_line": map[string]any{"type": "integer", "description": ""},
			"max_lines":  map[string]any{"type": "integer", "description": ""},
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
	path, err := resolveReadableFile(t.Paths, args.Path)
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
	content := b.String()
	if repoDir := findRepoRoot(t.Paths.Roots, filepath.Dir(path)); repoDir != "" {
		if warn := repoStaleSummary(repoDir); warn != "" {
			content = warn + content
		}
	}
	return registry.Result{Content: content}, nil
}

type SearchTool struct {
	Paths safety.WorkspacePolicy
}

func (SearchTool) Parallel() bool { return true }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-search",
		"",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": ""},
			"path":  map[string]any{"type": "string", "description": ""},
			"glob":  map[string]any{"type": "string", "description": ""},
			"limit": map[string]any{"type": "integer", "description": ""},
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
	path, err := resolveWorkspacePath(t.Paths, root)
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
	result := strings.Join(lines, "\n")
	// Prepend stale warning for each distinct repo found in the results.
	result = prependStaleWarnings(t.Paths.Roots, path, result)
	return registry.Result{Content: result}, nil
}

func resolveReadableFile(paths safety.WorkspacePolicy, path string) (string, error) {
	resolved, err := paths.ResolveReadableFile(path)
	if err == nil {
		return resolved, nil
	}
	if stripped, ok := stripWorkspaceRootBase(paths, path); ok {
		if resolved, retryErr := paths.ResolveReadableFile(stripped); retryErr == nil {
			return resolved, nil
		}
	}
	return "", err
}

func resolveWorkspacePath(paths safety.WorkspacePolicy, path string) (string, error) {
	resolved, err := paths.Resolve(path)
	if err == nil {
		return resolved, nil
	}
	if stripped, ok := stripWorkspaceRootBase(paths, path); ok {
		if resolved, retryErr := paths.Resolve(stripped); retryErr == nil {
			return resolved, nil
		}
	}
	return "", err
}

func stripWorkspaceRootBase(paths safety.WorkspacePolicy, path string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || filepath.IsAbs(clean) {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if len(parts) < 2 {
		return "", false
	}
	first := parts[0]
	for _, root := range paths.Roots {
		if filepath.Base(filepath.Clean(root)) == first {
			return filepath.FromSlash(strings.Join(parts[1:], "/")), true
		}
	}
	return "", false
}

// prependStaleWarnings checks the repo (or repos found in the search root) for
// stale working trees and prepends a warning to result if any are behind.
func prependStaleWarnings(workspaceRoots []string, searchRoot, result string) string {
	// Determine which repos to check: if searchRoot is inside a specific sub-repo,
	// only check that one; otherwise check all immediate sub-repos of each workspace root.
	checked := map[string]bool{}
	var warns []string
	check := func(dir string) {
		clean := filepath.Clean(dir)
		if checked[clean] {
			return
		}
		checked[clean] = true
		if w := repoStaleSummary(clean); w != "" {
			warns = append(warns, w)
		}
	}
	repoDir := findRepoRoot(workspaceRoots, searchRoot)
	if repoDir != "" {
		check(repoDir)
	} else {
		// Search spans multiple repos — check each workspace root's sub-repos.
		for _, root := range workspaceRoots {
			entries, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					check(filepath.Join(root, e.Name()))
				}
			}
		}
	}
	if len(warns) == 0 {
		return result
	}
	return strings.Join(warns, "") + result
}

// repoStaleSummary returns a warning banner when the git working tree at dir
// is detectably behind its upstream tracking branch.  It runs quickly (no
// network) and returns "" on any error or when the tree is current.
func repoStaleSummary(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir,
		"rev-list", "--count", "HEAD..@{u}").Output()
	if err != nil {
		return ""
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n <= 0 {
		return ""
	}
	// Identify the repo name from the directory.
	name := filepath.Base(filepath.Clean(dir))
	return fmt.Sprintf(
		"[STALE WORKING TREE: %s is %d commit(s) behind its upstream — "+
			"use repo-search or git-read_file_ref(ref=origin/main) for current code]\n",
		name, n,
	)
}

// findRepoRoot walks upward from absPath looking for a .git directory that
// is a sub-directory of one of the allowed workspace roots.
func findRepoRoot(roots []string, absPath string) string {
	dir := absPath
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			clean := filepath.Clean(dir)
			for _, root := range roots {
				cleanRoot := filepath.Clean(root)
				if clean == cleanRoot {
					return clean
				}
				if strings.HasPrefix(clean+"/", cleanRoot+"/") {
					return clean
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
