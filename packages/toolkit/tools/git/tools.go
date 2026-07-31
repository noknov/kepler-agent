package git

import (
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
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type Base struct {
	Paths   safety.WorkspacePolicy
	Guard   safety.CommandPolicy
	Timeout time.Duration
}

type StatusTool struct{ Base }

func (StatusTool) Parallel() bool { return true }

func (t StatusTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-status",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"repo": map[string]any{"type": "string", "description": ""},
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
		"Fetch origin refs and resolve a branch to an immutable commit SHA. Use this first when investigating a specific branch; then pass the returned repo/ref to git-search_ref or git-read_file_ref. This never checks out or updates the working tree, so multiple users can inspect different branches concurrently.",
		registry.ObjectSchema(nil, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit to use origin/HEAD, then mt-main/main/master fallback."},
		}),
	)
}

func (t FetchRefTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal(raw, &args)
	repo, err := t.repo(args.Repo)
	if err != nil {
		return registry.Result{}, err
	}
	snap, err := t.fetchSnapshot(ctx, repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: snap.header()}, nil
}

type snapshot struct {
	Repo        string
	Branch      string
	BranchRef   string
	Ref         string
	Commit      string
	FetchStatus string
}

type SearchRefTool struct{ Base }

func (SearchRefTool) Parallel() bool { return true }

func (t SearchRefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-search_ref",
		"Search an immutable git ref returned by git-fetch_ref. Use for branch-specific code investigation without changing the checkout. Search hits are hints; read matching ranges before claiming behavior.",
		registry.ObjectSchema([]string{"repo", "query", "ref"}, map[string]any{
			"repo":  map[string]any{"type": "string", "description": "Repository returned by git-fetch_ref."},
			"ref":   map[string]any{"type": "string", "description": "Immutable commit SHA returned by git-fetch_ref, or an explicit safe ref."},
			"query": map[string]any{"type": "string", "description": "Pattern to search."},
			"path":  map[string]any{"type": "string", "description": "Optional path inside the repo."},
			"limit": map[string]any{"type": "integer", "description": "Maximum matches, default 50 and max 200."},
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
	repo, err := t.explicitRepo(args.Repo)
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

type RepoSearchTool struct{ Base }

func (RepoSearchTool) Parallel() bool { return true }

func (t RepoSearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"repo-search",
		"Search the refreshed remote branch snapshot for a repo. Omit branch for origin/HEAD, mt-main/main/master fallback. This reads origin refs and never checks out the branch, so concurrent users can inspect different branches safely.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for default branch."},
			"query":  map[string]any{"type": "string", "description": "Pattern to search."},
			"path":   map[string]any{"type": "string", "description": "Optional path inside the repo."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum matches, default 50 and max 200."},
		}),
	)
}

func (t RepoSearchTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Query  string `json:"query"`
		Path   string `json:"path"`
		Limit  int    `json:"limit"`
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
	snap, err := t.fetchSnapshot(ctx, repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	path, err := cleanGitPath(args.Path)
	if err != nil {
		return registry.Result{}, err
	}
	cmdArgs := []string{"grep", "-n", "--no-color", "-I", "-e", args.Query, snap.Ref, "--"}
	if path != "" {
		cmdArgs = append(cmdArgs, path)
	}
	out, err := t.run(ctx, repo, cmdArgs...)
	if err != nil {
		if strings.TrimSpace(out) == "" {
			return registry.Result{Content: snap.header() + "\n\nno matches"}, nil
		}
		return registry.Result{}, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, snap.Ref+":")
	}
	if len(lines) > args.Limit {
		lines = append(lines[:args.Limit], "...[truncated after "+strconv.Itoa(args.Limit)+" matches]")
	}
	return registry.Result{Content: snap.header() + "\n\n" + strings.Join(lines, "\n")}, nil
}

type ReadFileRefTool struct{ Base }

func (ReadFileRefTool) Parallel() bool { return true }

func (t ReadFileRefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-read_file_ref",
		"Read a file at an immutable git ref returned by git-fetch_ref. Use this for branch-specific evidence without changing the checkout.",
		registry.ObjectSchema([]string{"repo", "ref", "path"}, map[string]any{
			"repo":       map[string]any{"type": "string", "description": "Repository returned by git-fetch_ref."},
			"ref":        map[string]any{"type": "string", "description": "Immutable commit SHA returned by git-fetch_ref, or an explicit safe ref."},
			"path":       map[string]any{"type": "string", "description": "Path inside the repo."},
			"start_line": map[string]any{"type": "integer", "description": "1-based starting line."},
			"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines, default 240 and max 1000."},
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
	repo, err := t.explicitRepo(args.Repo)
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

type RepoReadFileTool struct{ Base }

func (RepoReadFileTool) Parallel() bool { return true }

func (t RepoReadFileTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"repo-read_file",
		"Read a file from the refreshed remote branch snapshot. Omit branch for origin/HEAD, mt-main/main/master fallback. This never checks out or updates the working tree.",
		registry.ObjectSchema([]string{"path"}, map[string]any{
			"repo":       map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch":     map[string]any{"type": "string", "description": "Remote branch name. Omit for default branch."},
			"path":       map[string]any{"type": "string", "description": "Path inside the repo."},
			"start_line": map[string]any{"type": "integer", "description": "1-based starting line."},
			"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines, default 240 and max 1000."},
		}),
	)
}

func (t RepoReadFileTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo      string `json:"repo"`
		Branch    string `json:"branch"`
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
	snap, err := t.fetchSnapshot(ctx, repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.readAtRef(ctx, repo, snap.Ref, args.Path, args.StartLine, args.MaxLines)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: snap.header() + "\n\n" + out}, nil
}

type LogTool struct{ Base }

func (LogTool) Parallel() bool { return true }

func (t LogTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-log",
		"Show commit history from a refreshed remote branch snapshot, including commit hash, author name/email, author date, and subject. Pass branch when the user asks about a specific branch's latest commits; this never checks out or updates the working tree.",
		registry.ObjectSchema(nil, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum commits, default 10 and max 50."},
		}),
	)
}

func (t LogTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Limit  int    `json:"limit"`
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
	snap, err := t.fetchSnapshot(ctx, repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	format := "%h%x09%an%x09%ae%x09%ad%x09%s"
	out, err := t.run(ctx, repo, "log", "--date=iso-strict", "--format="+format, "-n", strconv.Itoa(args.Limit), snap.Ref)
	return registry.Result{Content: snap.header() + "\n\n" + out}, err
}

type ShowTool struct{ Base }

func (ShowTool) Parallel() bool { return true }

func (t ShowTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"git-show",
		"",
		registry.ObjectSchema([]string{"rev"}, map[string]any{
			"repo":      map[string]any{"type": "string", "description": ""},
			"rev":       map[string]any{"type": "string", "description": ""},
			"path":      map[string]any{"type": "string", "description": ""},
			"max_chars": map[string]any{"type": "integer", "description": ""},
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
	resolved, err := b.Paths.Resolve(path)
	if err != nil {
		return "", err
	}
	if isGitDir(resolved) {
		return resolved, nil
	}
	// The resolved path is not a git repo — scan immediate subdirectories
	// for a single repo (common multi-repo workspace layout).
	entries, dirErr := os.ReadDir(resolved)
	if dirErr != nil {
		return resolved, nil
	}
	var repos []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sub := filepath.Join(resolved, e.Name())
		if isGitDir(sub) {
			repos = append(repos, sub)
		}
	}
	if len(repos) == 1 {
		return repos[0], nil
	}
	if len(repos) > 1 {
		names := make([]string, len(repos))
		for i, r := range repos {
			names[i] = filepath.Base(r)
		}
		return "", fmt.Errorf("workspace contains multiple repos — pass repo=%s", strings.Join(names, " | "))
	}
	return resolved, nil
}

func isGitDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

func (b Base) explicitRepo(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("repo is required for ref-based git tools; use the same repo returned by git-fetch_ref")
	}
	return b.Paths.Resolve(path)
}

func (b Base) fetchSnapshot(ctx context.Context, repo, rawBranch string, rt registry.Runtime) (snapshot, error) {
	branch := strings.TrimSpace(rawBranch)
	fetchCtx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()
	fetchStatus := "origin_refs_current_or_recent"
	var fetchErr error
	if branch != "" {
		fetchErr = gitcache.FetchOriginFresh(fetchCtx, repo)
		fetchStatus = "origin_refs_refreshed"
	} else {
		// Default-branch reads keep the short process-wide TTL to avoid paying a
		// network fetch for every repo-search/repo-read_file pair.
		fetchErr = gitcache.FetchOrigin(fetchCtx, repo, gitcache.DefaultFetchTTL)
	}
	if fetchErr != nil {
		fetchStatus = "refresh_failed_using_cached_refs: " + fetchErr.Error()
	}
	if branch == "" {
		var err error
		branch, err = b.defaultBranch(ctx, repo)
		if err != nil {
			return snapshot{}, err
		}
	}
	if err := validateRefPart(branch); err != nil {
		return snapshot{}, err
	}
	ref := "origin/" + branch
	if !b.refExists(ctx, repo, ref) {
		if strings.HasPrefix(fetchStatus, "refresh_failed") {
			return snapshot{}, fmt.Errorf("%s; branch %q is not available in cached origin refs", fetchStatus, branch)
		}
		return snapshot{}, fmt.Errorf("branch %q does not exist on origin", branch)
	}
	commit, err := b.run(ctx, repo, "rev-parse", ref)
	if err != nil {
		return snapshot{}, err
	}
	short, err := b.run(ctx, repo, "rev-parse", "--short", ref)
	if err != nil {
		return snapshot{}, err
	}
	snap := snapshot{
		Repo:        repoLabel(repo),
		Branch:      branch,
		BranchRef:   ref,
		Ref:         strings.TrimSpace(commit),
		Commit:      strings.TrimSpace(short),
		FetchStatus: fetchStatus,
	}
	cacheKey := "git-snapshot\x00" + repo + "\x00" + branch
	rt.Cache.Set(cacheKey, snap)
	if rawBranch == "" {
		rt.Cache.Set("git-snapshot\x00"+repo+"\x00", snap)
	}
	return snap, nil
}

func (s snapshot) header() string {
	return fmt.Sprintf("repo=%s\nbranch=%s\nbranch_ref=%s\nref=%s\ncommit=%s\nfetch_status=%s\nworking_tree_changed=false", s.Repo, s.Branch, s.BranchRef, s.Ref, s.Commit, s.FetchStatus)
}

func (b Base) defaultBranch(ctx context.Context, repo string) (string, error) {
	if out, err := b.run(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if branch := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); branch != "" {
			return branch, nil
		}
	}
	for _, branch := range []string{"mt-main", "main", "master"} {
		if b.refExists(ctx, repo, "origin/"+branch) || b.refExists(ctx, repo, branch) {
			return branch, nil
		}
	}
	return "", fmt.Errorf("could not find default branch from origin/HEAD, main, or master")
}

func (b Base) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 30 * time.Second
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

func (b Base) readAtRef(ctx context.Context, repo, ref, rawPath string, startLine, maxLines int) (string, error) {
	path, err := cleanGitPath(rawPath)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if startLine <= 0 {
		startLine = 1
	}
	if maxLines <= 0 {
		maxLines = 240
	}
	if maxLines > 1000 {
		maxLines = 1000
	}
	out, err := b.run(ctx, repo, "show", ref+":"+path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(out, "\n")
	var buf strings.Builder
	emitted := 0
	for idx, line := range lines {
		lineNo := idx + 1
		if lineNo < startLine {
			continue
		}
		if emitted >= maxLines {
			break
		}
		buf.WriteString(fmt.Sprintf("%6d  %s\n", lineNo, line))
		emitted++
		if buf.Len() > 200_000 {
			buf.WriteString("...[truncated]\n")
			break
		}
	}
	return buf.String(), nil
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

func repoLabel(repo string) string {
	return filepath.ToSlash(filepath.Base(repo))
}

func (b Base) run(ctx context.Context, repo string, args ...string) (string, error) {
	if err := b.Guard.CheckArgv(append([]string{"git", "-C", repo}, args...)); err != nil {
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
