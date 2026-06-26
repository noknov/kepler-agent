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

	// If the file is inside a tracked git repo, lazy-fetch once per run and
	// read from the upstream tracking ref via "git show".  This guarantees the
	// agent always sees the latest committed code without modifying the working
	// tree — safe for concurrent multi-user access.
	if repoDir := findRepoRoot(t.Paths.Roots, filepath.Dir(path)); repoDir != "" {
		upstreamRef := lazyFetchRepo(ctx, repoDir, rt)
		if relPath, relErr := filepath.Rel(repoDir, path); relErr == nil && !strings.HasPrefix(relPath, "..") {
			content, gitErr := gitShowFile(ctx, repoDir, upstreamRef, relPath)
			if gitErr == nil {
				header := fmt.Sprintf("[source: git %s]\n", upstreamRef)
				return registry.Result{Content: header + applyLineRange(content, args.StartLine, args.MaxLines)}, nil
			}
			// Fall through on git error (uncommitted file, different branch, etc.)
		}
	}

	// Non-git file or git-show failed: read from working tree.
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
	searchPath, err := resolveWorkspacePath(t.Paths, root)
	if err != nil {
		return registry.Result{}, err
	}

	// Find all git repos covering the search path and use "git grep" on the
	// upstream tracking ref. This never touches the working tree and is safe
	// for concurrent multi-user access even when users target different branches.
	repos := findReposUnder(t.Paths.Roots, searchPath)
	if len(repos) > 0 {
		var lines []string
		for _, repoDir := range repos {
			upstreamRef := lazyFetchRepo(ctx, repoDir, rt)
			// Compute the pathspec relative to the repo root.
			relSearch := "."
			if rel, relErr := filepath.Rel(repoDir, searchPath); relErr == nil &&
				rel != "." && !strings.HasPrefix(rel, "..") {
				relSearch = rel
			}
			remaining := args.Limit - len(lines)
			if remaining <= 0 {
				break
			}
			got, grepErr := gitGrep(ctx, repoDir, upstreamRef, args.Query, relSearch, args.Glob, remaining)
			if grepErr != nil {
				continue
			}
			// git grep output for tree searches: "<ref>:<path>:<linenum>:<content>"
			// Strip the "<ref>:" prefix and prepend the repo name so the format
			// matches what rg used to emit after workspace-root stripping.
			repoName := filepath.Base(repoDir)
			refPrefix := upstreamRef + ":"
			for _, l := range got {
				l = strings.TrimPrefix(l, refPrefix)
				lines = append(lines, repoName+"/"+l)
			}
		}
		if len(lines) == 0 {
			return registry.Result{Content: "no matches"}, nil
		}
		if len(lines) > args.Limit {
			lines = append(lines[:args.Limit], "...[truncated after "+strconv.Itoa(args.Limit)+" matches]")
		}
		return registry.Result{Content: strings.Join(lines, "\n")}, nil
	}

	// No git repos found — fall back to rg on the local working tree.
	cmdArgs := []string{"--line-number", "--no-heading", "--color=never"}
	if args.Glob != "" {
		cmdArgs = append(cmdArgs, "--glob", args.Glob)
	}
	cmdArgs = append(cmdArgs, "--", args.Query, searchPath)
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
	for _, r := range t.Paths.Roots {
		output = strings.ReplaceAll(output, r+"/", "")
	}
	resultLines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(resultLines) > args.Limit {
		resultLines = append(resultLines[:args.Limit], "...[truncated after "+strconv.Itoa(args.Limit)+" matches]")
	}
	return registry.Result{Content: strings.Join(resultLines, "\n")}, nil
}

// lazyFetchRepo does one "git fetch origin" per repo per agent run, deduplicating
// via rt.Cache. Returns the upstream tracking ref for HEAD (e.g. "origin/main").
// This is the lazy-load contract: the first code-search or code-read_file call for
// a repo in a given run triggers a fetch; subsequent calls skip the network hop.
func lazyFetchRepo(ctx context.Context, repoDir string, rt registry.Runtime) string {
	cacheKey := "code-git-fetch\x00" + filepath.Clean(repoDir)
	if _, ok := rt.Cache.Get(cacheKey); !ok {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(fetchCtx, "git", "-C", repoDir,
			"fetch", "--prune", "--force", "--no-write-fetch-head", "origin")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		_, _ = cmd.CombinedOutput() // non-fatal; stale is always better than broken
		rt.Cache.Set(cacheKey, true)
	}
	// Resolve the upstream tracking ref (fast, no network).
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "origin/main"
	}
	return strings.TrimSpace(string(out))
}

// gitShowFile returns the full text content of relPath inside repoDir at ref.
func gitShowFile(ctx context.Context, repoDir, ref, relPath string) (string, error) {
	arg := ref + ":" + filepath.ToSlash(relPath)
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "show", arg).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// applyLineRange formats raw file content with 1-based line numbers, applying
// the given start/max window.
func applyLineRange(content string, startLine, maxLines int) string {
	var b strings.Builder
	lineNo := 0
	emitted := 0
	for _, line := range strings.Split(content, "\n") {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if emitted >= maxLines {
			break
		}
		b.WriteString(fmt.Sprintf("%6d  %s\n", lineNo, line))
		emitted++
		if b.Len() > 200_000 {
			b.WriteString("...[truncated]\n")
			break
		}
	}
	return b.String()
}

// gitGrep searches repoDir at ref for query using "git grep -E", returning
// the raw output lines. relPath restricts the search to a sub-directory;
// pass "." to search the whole repo. glob filters by file pattern.
func gitGrep(ctx context.Context, repoDir, ref, query, relPath, glob string, limit int) ([]string, error) {
	cmdArgs := []string{"-C", repoDir, "grep", "--line-number", "-E", query, ref, "--"}
	if glob != "" {
		// Convert rg-style glob to git pathspec magic; add **/ prefix when absent.
		g := glob
		if !strings.HasPrefix(g, "**/") {
			g = "**/" + g
		}
		cmdArgs = append(cmdArgs, ":(glob)"+g)
	}
	if relPath != "." && relPath != "" {
		cmdArgs = append(cmdArgs, relPath)
	}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, fmt.Errorf("git grep: %s", strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

// findReposUnder returns the git repos that cover dir: if dir is inside a repo,
// returns that single repo; otherwise enumerates immediate sub-directories for repos.
func findReposUnder(workspaceRoots []string, dir string) []string {
	if repoDir := findRepoRoot(workspaceRoots, dir); repoDir != "" {
		return []string{repoDir}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var repos []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if repoDir := findRepoRoot(workspaceRoots, sub); repoDir != "" && !seen[repoDir] {
			seen[repoDir] = true
			repos = append(repos, repoDir)
		}
	}
	return repos
}

// findRepoRoot walks upward from absPath looking for a .git directory that
// is within one of the allowed workspace roots.
func findRepoRoot(roots []string, absPath string) string {
	dir := absPath
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			clean := filepath.Clean(dir)
			for _, root := range roots {
				cleanRoot := filepath.Clean(root)
				if clean == cleanRoot || strings.HasPrefix(clean+"/", cleanRoot+"/") {
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
