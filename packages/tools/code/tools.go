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

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/gitcache"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

type ReadFileTool struct {
	Paths safety.WorkspacePolicy
}

func (ReadFileTool) Parallel() bool { return true }

func (t ReadFileTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-read_file",
		"Read source code from a freshly fetched git ref. Omit source to use the file repo's origin/<current branch> ref; use source=working_tree only for explicit local uncommitted changes. Results include line numbers and source metadata; cite these lines before making code behavior claims.",
		registry.ObjectSchema([]string{"path"}, map[string]any{
			"path":       map[string]any{"type": "string", "description": "Workspace-relative, root-prefixed, or absolute file path."},
			"source":     map[string]any{"type": "string", "description": "Optional: current_branch (default, origin/<current branch>), working_tree for explicit local changes, or an explicit safe git ref such as origin/main or a commit SHA."},
			"start_line": map[string]any{"type": "integer", "description": "1-based starting line. Omit unless you already know the relevant range."},
			"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines to return, default 240 and max 1000."},
		}),
	)
}

func (t ReadFileTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Path      string `json:"path"`
		Source    string `json:"source"`
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

	source := strings.TrimSpace(args.Source)
	if source == "" {
		source = "current_branch"
	}
	repoDir := findRepoRoot(t.Paths.Roots, filepath.Dir(path))
	if source != "working_tree" {
		if repoDir == "" {
			return registry.Result{}, fmt.Errorf("source %q requires a file inside a git repository", source)
		}
		ref, fetchStatus, sourceErr := sourceRef(ctx, repoDir, source, rt)
		if sourceErr != nil {
			return registry.Result{}, sourceErr
		}
		if relPath, relErr := filepath.Rel(repoDir, path); relErr == nil && !strings.HasPrefix(relPath, "..") {
			content, gitErr := gitShowFile(ctx, repoDir, ref, relPath)
			if gitErr == nil {
				commit := gitRevParse(ctx, repoDir, ref, "--short")
				header := fmt.Sprintf("[source: git ref=%s commit=%s fetch_status=%s]\n", ref, commit, fetchStatus)
				recordReadState(rt, readState{
					Path:      filepath.ToSlash(relPath),
					Repo:      filepath.Base(repoDir),
					Source:    "git",
					Ref:       ref,
					Commit:    commit,
					StartLine: args.StartLine,
					MaxLines:  args.MaxLines,
				})
				return registry.Result{Content: header + applyLineRange(content, args.StartLine, args.MaxLines)}, nil
			}
			return registry.Result{}, fmt.Errorf("read %s at %s: %w", filepath.ToSlash(relPath), ref, gitErr)
		}
		return registry.Result{}, fmt.Errorf("path %q is outside repository %q", path, repoDir)
	}

	// Working-tree access is explicit, including for files outside git repos.
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
	header := "[source: working_tree"
	if repoDir != "" {
		header += " branch=" + gitRevParse(ctx, repoDir, "HEAD", "--abbrev-ref")
	}
	header += "]\n"
	recordReadState(rt, readState{Path: path, Source: "working_tree", StartLine: args.StartLine, MaxLines: args.MaxLines})
	return registry.Result{Content: header + b.String()}, nil
}

type SearchTool struct {
	Paths safety.WorkspacePolicy
}

func (SearchTool) Parallel() bool { return true }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"code-search",
		"Search source code from freshly fetched git refs. Omit source to use each repo's origin/<current branch> ref; use source=working_tree only for explicit local uncommitted changes. Search hits are hints; read the matching file/range with code-read_file before claiming behavior.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query":         map[string]any{"type": "string", "description": "Regex or literal pattern to search for."},
			"path":          map[string]any{"type": "string", "description": "Optional workspace-relative directory or file to search."},
			"glob":          map[string]any{"type": "string", "description": "Optional file glob, for example **/*.go."},
			"source":        map[string]any{"type": "string", "description": "Optional: current_branch (default, origin/<current branch>), working_tree for explicit local changes, or an explicit safe git ref such as origin/main or a commit SHA."},
			"context_lines": map[string]any{"type": "integer", "description": "Optional lines of context around matches, max 5."},
			"limit":         map[string]any{"type": "integer", "description": "Maximum matching lines, default 50 and max 200."},
		}),
	)
}

func (t SearchTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Query        string `json:"query"`
		Path         string `json:"path"`
		Glob         string `json:"glob"`
		Source       string `json:"source"`
		ContextLines int    `json:"context_lines"`
		Limit        int    `json:"limit"`
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
	if args.ContextLines < 0 {
		args.ContextLines = 0
	}
	if args.ContextLines > 5 {
		args.ContextLines = 5
	}
	source := strings.TrimSpace(args.Source)
	if source == "" {
		source = "current_branch"
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
	if len(repos) > 0 && source != "working_tree" {
		var lines []string
		var headers []string
		var grepErrors []string
		for _, repoDir := range repos {
			ref, fetchStatus, sourceErr := sourceRef(ctx, repoDir, source, rt)
			if sourceErr != nil {
				return registry.Result{}, sourceErr
			}
			headers = append(headers, fmt.Sprintf("[source: git repo=%s ref=%s commit=%s fetch_status=%s]", filepath.Base(repoDir), ref, gitRevParse(ctx, repoDir, ref, "--short"), fetchStatus))
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
			got, grepErr := gitGrep(ctx, repoDir, ref, args.Query, relSearch, args.Glob, args.ContextLines, remaining)
			if grepErr != nil {
				grepErrors = append(grepErrors, fmt.Sprintf("%s: %v", filepath.Base(repoDir), grepErr))
				continue
			}
			// git grep output for tree searches: "<ref>:<path>:<linenum>:<content>"
			// Strip the "<ref>:" prefix and prepend the repo name so the format
			// matches what rg used to emit after workspace-root stripping.
			repoName := filepath.Base(repoDir)
			refPrefix := ref + ":"
			for _, l := range got {
				l = strings.TrimPrefix(l, refPrefix)
				lines = append(lines, repoName+"/"+l)
			}
		}
		if len(lines) == 0 {
			if len(grepErrors) > 0 {
				return registry.Result{}, fmt.Errorf("code search failed: %s", strings.Join(grepErrors, "; "))
			}
			return registry.Result{Content: strings.Join(headers, "\n") + "\n\nno matches"}, nil
		}
		if len(lines) > args.Limit {
			lines = append(lines[:args.Limit], "...[truncated after "+strconv.Itoa(args.Limit)+" matches]")
		}
		return registry.Result{Content: strings.Join(headers, "\n") + "\n\nSearch hits are hints; read matching files before claiming behavior.\n" + strings.Join(lines, "\n")}, nil
	}
	if source != "working_tree" {
		return registry.Result{}, fmt.Errorf("source %q requires a search path containing a git repository", source)
	}

	// Working-tree search is explicit and uses rg locally.
	cmdArgs := []string{"--line-number", "--no-heading", "--color=never"}
	if args.ContextLines > 0 {
		cmdArgs = append(cmdArgs, "-C", strconv.Itoa(args.ContextLines))
	}
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
	return registry.Result{Content: "[source: working_tree]\n\nSearch hits are hints; read matching files before claiming behavior.\n" + strings.Join(resultLines, "\n")}, nil
}

func sourceRef(ctx context.Context, repoDir, source string, rt registry.Runtime) (string, string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "current_branch"
	}
	if err := refreshRepo(ctx, repoDir, rt); err != nil {
		return "", "", err
	}
	fetchStatus := "origin_refs_refreshed"
	if source == "current_branch" {
		ref, err := currentBranchOriginRef(ctx, repoDir)
		return ref, fetchStatus, err
	}
	if !safeGitRef(source) {
		return "", fetchStatus, fmt.Errorf("invalid git source ref %q", source)
	}
	return source, fetchStatus, nil
}

func currentBranchOriginRef(ctx context.Context, repoDir string) (string, error) {
	branch := gitRevParse(ctx, repoDir, "HEAD", "--abbrev-ref")
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("current_branch source requires a checked-out branch")
	}
	ref := "origin/" + branch
	if !safeGitRef(ref) {
		return "", fmt.Errorf("invalid current branch ref %q", ref)
	}
	if commit := gitRevParse(ctx, repoDir, ref, "--verify"); commit == "" {
		return "", fmt.Errorf("current_branch source requires refreshed remote ref %q", ref)
	}
	return ref, nil
}

func refreshRepo(ctx context.Context, repoDir string, rt registry.Runtime) error {
	cacheKey := "code-git-fetch\x00" + filepath.Clean(repoDir)
	if _, ok := rt.Cache.Get(cacheKey); !ok {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := gitcache.FetchOriginFresh(fetchCtx, repoDir); err != nil {
			return fmt.Errorf("refresh origin refs: %w", err)
		}
		rt.Cache.Set(cacheKey, true)
	}
	return nil
}

func safeGitRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" &&
		!strings.Contains(ref, "..") &&
		!strings.HasPrefix(ref, "-") &&
		!strings.ContainsAny(ref, " \t\n\r~^:?*[\\")
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

func gitRevParse(ctx context.Context, repoDir, ref string, extra ...string) string {
	args := append([]string{"-C", repoDir, "--no-optional-locks", "rev-parse"}, extra...)
	args = append(args, ref)
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
func gitGrep(ctx context.Context, repoDir, ref, query, relPath, glob string, contextLines, limit int) ([]string, error) {
	cmdArgs := []string{"-C", repoDir, "grep", "--line-number", "-E"}
	if contextLines > 0 {
		cmdArgs = append(cmdArgs, "-C", strconv.Itoa(contextLines))
	}
	cmdArgs = append(cmdArgs, "-e", query, ref, "--")
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

type readState struct {
	Path      string
	Repo      string
	Source    string
	Ref       string
	Commit    string
	StartLine int
	MaxLines  int
}

func recordReadState(rt registry.Runtime, state readState) {
	if rt.Cache == nil {
		return
	}
	const key = "code-read-state"
	var states []readState
	if existing, ok := rt.Cache.Get(key); ok {
		if typed, ok := existing.([]readState); ok {
			states = typed
		}
	}
	states = append(states, state)
	if len(states) > 100 {
		states = states[len(states)-100:]
	}
	rt.Cache.Set(key, states)
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
			if realClean, err := filepath.EvalSymlinks(clean); err == nil {
				clean = realClean
			}
			for _, root := range roots {
				cleanRoot := filepath.Clean(root)
				if realRoot, err := filepath.EvalSymlinks(cleanRoot); err == nil {
					cleanRoot = realRoot
				}
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
