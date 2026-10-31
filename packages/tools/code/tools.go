package code

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/toolkit/gitcache"
)

type ReadFileTool struct {
	Paths safety.WorkspacePolicy
}

func (t ReadFileTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-read_file",
		"Read source code from a freshly fetched git ref. Omit source for normal repository investigation so the tool resolves each file repo's origin-tracked upstream. Set source only when the user explicitly requests the checkout view or an exact git ref; never invent a branch or ref. Results include line numbers and source metadata; cite these lines before making code behavior claims.",
		tool.ObjectSchema(nil, map[string]any{
			"path":       map[string]any{"type": "string", "description": "Workspace-relative, root-prefixed, or absolute file path."},
			"paths":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Independent files to read in this call. Use this instead of serial single-file calls when the files do not depend on each other."},
			"source":     map[string]any{"type": "string", "description": "Optional; omit for normal investigation. Set to working_tree only for an explicitly requested checkout view, or to an exact safe git ref explicitly named by the user."},
			"start_line": map[string]any{"type": "integer", "description": "1-based starting line. Omit unless you already know the relevant range."},
			"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines to return per file, default 240 and max 1000."},
		}),
		tool.WithEffects(tool.EffectRead),
	)
}

func (t ReadFileTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Path      string   `json:"path"`
		Paths     []string `json:"paths"`
		Source    string   `json:"source"`
		StartLine int      `json:"start_line"`
		MaxLines  int      `json:"max_lines"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	files := tool.NormalizePaths(args.Path, args.Paths)
	if len(files) == 0 {
		return tool.Result{}, fmt.Errorf("path or paths is required")
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
	text, err := tool.MapOrdered(len(files), func(i int) (string, error) {
		body, readErr := t.readOne(ctx, call, files[i], source, args.StartLine, args.MaxLines)
		if readErr != nil {
			return "", readErr
		}
		if len(files) == 1 {
			return body, nil
		}
		return "## " + files[i] + "\n" + body, nil
	})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(text), nil
}

func (t ReadFileTool) readOne(ctx context.Context, call tool.Call, rawPath, source string, startLine, maxLines int) (string, error) {
	if safety.IsSensitivePath(rawPath) {
		return "", fmt.Errorf("refusing to read sensitive file %q", filepath.Base(rawPath))
	}
	path, err := resolveReadableFile(t.Paths, rawPath)
	if err != nil {
		return "", err
	}
	repoDir := findRepoRoot(t.Paths.Roots, filepath.Dir(path))
	if source != "working_tree" {
		if repoDir == "" {
			return "", fmt.Errorf("source %q requires a file inside a git repository", source)
		}
		snapshot, sourceErr := resolveSource(ctx, repoDir, source, call.Scope)
		if sourceErr != nil {
			return "", sourceErr
		}
		if relPath, relErr := filepath.Rel(repoDir, path); relErr == nil && filepath.IsLocal(relPath) {
			content, gitErr := gitShowFile(ctx, repoDir, snapshot.commit, relPath, startLine, maxLines)
			if gitErr == nil {
				header := fmt.Sprintf("[source: git ref=%s commit=%s fetch_status=%s]\n", snapshot.ref, snapshot.commit, snapshot.fetchStatus)
				return header + content, nil
			}
			return "", fmt.Errorf("read %s at %s: %w", filepath.ToSlash(relPath), snapshot.ref, gitErr)
		}
		return "", fmt.Errorf("path %q is outside repository %q", path, repoDir)
	}

	// Working-tree access is explicit, including for files outside git repos.
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	content, err := readLineRange(ctx, file, startLine, maxLines)
	if err != nil {
		return "", err
	}

	header := "[source: working_tree"
	if repoDir != "" {
		header += " branch=" + gitRevParse(ctx, repoDir, "HEAD", "--abbrev-ref")
	}
	header += "]\n"
	return header + content, nil
}

type SearchTool struct {
	Paths safety.WorkspacePolicy
}

func (t SearchTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"code-search",
		"Search source code from freshly fetched git refs. Omit source for normal repository investigation so each repo's origin-tracked upstream is resolved independently. Set source only when the user explicitly requests the checkout view or an exact git ref; never invent a branch or ref. Search hits are hints; read the matching file/range with code-read_file before claiming behavior.",
		tool.ObjectSchema([]string{"query"}, map[string]any{
			"query":         map[string]any{"type": "string", "description": "Literal text to search for by default. Use query_mode=regex only when regular-expression matching is required."},
			"query_mode":    map[string]any{"type": "string", "enum": []string{"literal", "regex"}, "description": "Search interpretation. Defaults to literal."},
			"path":          map[string]any{"type": "string", "description": "Optional workspace-relative directory or file to search."},
			"glob":          map[string]any{"type": "string", "description": "Optional file glob, for example **/*.go."},
			"source":        map[string]any{"type": "string", "description": "Optional; omit for normal investigation. Set to working_tree only for an explicitly requested checkout view, or to an exact safe git ref explicitly named by the user."},
			"context_lines": map[string]any{"type": "integer", "description": "Optional lines of context around matches, max 5."},
			"limit":         map[string]any{"type": "integer", "description": "Maximum matching lines, default 50 and max 200."},
		}),
		tool.WithEffects(tool.EffectRead),
	)
}

func (t SearchTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Query        string `json:"query"`
		QueryMode    string `json:"query_mode"`
		Path         string `json:"path"`
		Glob         string `json:"glob"`
		Source       string `json:"source"`
		ContextLines int    `json:"context_lines"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(args.Query) == "" {
		return tool.Result{}, fmt.Errorf("query is required")
	}
	queryMode := strings.TrimSpace(args.QueryMode)
	if queryMode == "" {
		queryMode = "literal"
	}
	if queryMode != "literal" && queryMode != "regex" {
		return tool.Result{}, fmt.Errorf("query_mode must be literal or regex")
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
		return tool.Result{}, err
	}

	// Find all git repos covering the search path and use "git grep" on the
	// upstream tracking ref. This never touches the working tree and is safe
	// for concurrent multi-user access even when users target different branches.
	repos := findReposUnder(t.Paths.Roots, searchPath)
	if len(repos) > 0 && source != "working_tree" {
		var lines []string
		var headers []string
		var grepErrors []string
		var truncated bool
		for _, repoDir := range repos {
			snapshot, sourceErr := resolveSource(ctx, repoDir, source, call.Scope)
			if sourceErr != nil {
				grepErrors = append(grepErrors, fmt.Sprintf("%s: %v", filepath.Base(repoDir), sourceErr))
				continue
			}
			headers = append(headers, fmt.Sprintf("[source: git repo=%s ref=%s commit=%s fetch_status=%s]", filepath.Base(repoDir), snapshot.ref, snapshot.commit, snapshot.fetchStatus))
			// Compute the pathspec relative to the repo root.
			relSearch := "."
			if rel, relErr := filepath.Rel(repoDir, searchPath); relErr == nil &&
				rel != "." && filepath.IsLocal(rel) {
				relSearch = rel
			}
			remaining := args.Limit - len(lines)
			if remaining <= 0 {
				truncated = true
				break
			}
			got, clipped, grepErr := gitGrep(ctx, repoDir, snapshot.commit, args.Query, queryMode, relSearch, args.Glob, args.ContextLines, remaining)
			if grepErr != nil {
				grepErrors = append(grepErrors, fmt.Sprintf("%s: %v", filepath.Base(repoDir), grepErr))
				continue
			}
			truncated = truncated || clipped
			// git grep output for tree searches: "<ref>:<path>:<linenum>:<content>"
			// Strip the "<ref>:" prefix and prepend the repo name so the format
			// matches what rg used to emit after workspace-root stripping.
			repoName := filepath.Base(repoDir)
			refPrefix := snapshot.commit + ":"
			for _, l := range got {
				l = strings.TrimPrefix(l, refPrefix)
				lines = append(lines, repoName+"/"+l)
			}
		}
		if len(lines) == 0 {
			if len(grepErrors) > 0 {
				return tool.Result{}, fmt.Errorf("code search failed: %s", strings.Join(grepErrors, "; "))
			}
			return tool.TextResult(strings.Join(headers, "\n") + "\n\nno matches"), nil
		}
		if len(lines) > args.Limit {
			lines = lines[:args.Limit]
			truncated = true
		}
		if truncated {
			lines = append(lines, "...[truncated by search output limit]")
		}
		content := strings.Join(headers, "\n") + "\n\nSearch hits are hints; read matching files before claiming behavior.\n" + strings.Join(lines, "\n")
		if len(grepErrors) > 0 {
			content += "\n\nSearch warnings:\n- " + strings.Join(grepErrors, "\n- ")
		}
		result := tool.TextResult(content)
		result.Truncated = truncated
		return result, nil
	}
	if source != "working_tree" {
		return tool.Result{}, fmt.Errorf("source %q requires a search path containing a git repository", source)
	}

	// Working-tree search is explicit and uses rg locally.
	cmdArgs := []string{"--line-number", "--no-heading", "--color=never"}
	if queryMode == "literal" {
		cmdArgs = append(cmdArgs, "--fixed-strings")
	}
	if args.ContextLines > 0 {
		cmdArgs = append(cmdArgs, "-C", strconv.Itoa(args.ContextLines))
	}
	if args.Glob != "" {
		cmdArgs = append(cmdArgs, "--glob", args.Glob)
	}
	cmdArgs = append(cmdArgs, "--", args.Query, searchPath)
	cmd := exec.CommandContext(ctx, "rg", cmdArgs...)
	var stdout cappedBuffer
	stdout.limit = 4 << 20
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return tool.TextResult("no matches"), nil
		}
		return tool.Result{}, fmt.Errorf("rg failed: %s", strings.TrimSpace(stderr.String()))
	}
	output := stdout.String()
	for _, r := range t.Paths.Roots {
		output = strings.ReplaceAll(output, r+"/", "")
	}
	resultLines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	truncated := stdout.truncated
	if len(resultLines) > args.Limit {
		resultLines = resultLines[:args.Limit]
		truncated = true
	}
	if truncated {
		resultLines = append(resultLines, "...[truncated by search output limit]")
	}
	result := tool.TextResult("[source: working_tree]\n\nSearch hits are hints; read matching files before claiming behavior.\n" + strings.Join(resultLines, "\n"))
	result.Truncated = truncated
	return result, nil
}

type sourceSnapshot struct {
	ref, commit, fetchStatus string
}

func resolveSource(ctx context.Context, repoDir, source string, scope tool.Scope) (sourceSnapshot, error) {
	if safety.IsSensitivePath(repoDir) {
		return sourceSnapshot{}, fmt.Errorf("refusing to search or read sensitive repository %q", filepath.Base(repoDir))
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "current_branch"
	}
	if err := refreshRepo(ctx, repoDir, scope); err != nil {
		return sourceSnapshot{}, err
	}
	if source == "current_branch" {
		var err error
		source, err = currentBranchOriginRef(ctx, repoDir)
		if err != nil {
			return sourceSnapshot{}, err
		}
	} else if !safeGitRef(source) {
		return sourceSnapshot{}, fmt.Errorf("invalid git source ref %q", source)
	}
	commit := gitRevParse(ctx, repoDir, source+"^{commit}", "--verify", "--end-of-options")
	if commit == "" {
		return sourceSnapshot{}, fmt.Errorf("cannot resolve git source ref %q to a commit", source)
	}
	return sourceSnapshot{ref: source, commit: commit, fetchStatus: "origin_refs_refreshed"}, nil
}

func currentBranchOriginRef(ctx context.Context, repoDir string) (string, error) {
	if upstream := gitRevParse(ctx, repoDir, "@{upstream}", "--abbrev-ref"); strings.HasPrefix(upstream, "origin/") && safeGitRef(upstream) {
		if commit := gitRevParse(ctx, repoDir, upstream, "--verify"); commit != "" {
			return upstream, nil
		}
	}
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

func refreshRepo(ctx context.Context, repoDir string, scope tool.Scope) error {
	cacheKey := "code-git-fetch\x00" + filepath.Clean(repoDir)
	if _, ok := tool.CacheFor(scope).Get(cacheKey); !ok {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := gitcache.FetchOriginFresh(fetchCtx, repoDir); err != nil {
			return fmt.Errorf("refresh origin refs: %w", err)
		}
		tool.CacheFor(scope).Set(cacheKey, true)
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

// gitShowFile streams the requested source window without retaining the full blob.
func gitShowFile(ctx context.Context, repoDir, ref, relPath string, startLine, maxLines int) (string, error) {
	arg := ref + ":" + filepath.ToSlash(relPath)
	command := exec.CommandContext(ctx, "git", "-C", repoDir, "show", arg)
	output, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", err
	}
	content, readErr := readLineRange(ctx, output, startLine, maxLines)
	// Drain unread bytes so Git can exit normally; keep its actual exit status.
	_, drainErr := io.Copy(io.Discard, output)
	waitErr := command.Wait()
	if readErr != nil {
		return "", readErr
	}
	if drainErr != nil {
		return "", drainErr
	}
	return content, waitErr
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

// readLineRange formats the requested 1-based source window with bounded memory.
func readLineRange(ctx context.Context, input io.Reader, startLine, maxLines int) (string, error) {
	reader := bufio.NewReaderSize(input, 64*1024)
	var output strings.Builder
	lineNo, emitted := 1, 0
	continued := false
	for emitted < maxLines {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fragment, more, err := reader.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if lineNo >= startLine {
			if !continued {
				fmt.Fprintf(&output, "%6d  ", lineNo)
			}
			remaining := max(0, 200_000-output.Len())
			if len(fragment) > remaining {
				output.Write(fragment[:remaining])
				output.WriteString("\n...[truncated]\n")
				return output.String(), nil
			}
			output.Write(fragment)
			if !more {
				output.WriteByte('\n')
				emitted++
			}
		}
		continued = more
		if !more {
			lineNo++
		}
	}
	return output.String(), nil
}

// Escape only the literal directory prefix; the user-supplied glob retains
// Git wildmatch semantics inside that directory.
var gitGlobPathLiteral = strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]")

func recursiveGitGlob(pattern string) string {
	if strings.HasPrefix(pattern, "**/") {
		return pattern
	}
	return "**/" + pattern
}

// gitGrep searches repoDir at ref for query, returning
// the raw output lines. relPath restricts the search to a sub-directory;
// pass "." to search the whole repo. glob filters by file pattern.
func gitGrep(ctx context.Context, repoDir, ref, query, queryMode, relPath, glob string, contextLines, limit int) ([]string, bool, error) {
	var pathspec string
	if relPath != "." && relPath != "" {
		relative := filepath.ToSlash(relPath)
		output, err := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "-t", ref+":"+relative).Output()
		if err != nil {
			return nil, false, fmt.Errorf("inspect source path %q: %w", relative, err)
		}
		switch strings.TrimSpace(string(output)) {
		case "blob":
			// Explicit files have priority over globs, as in working-tree searches.
			pathspec = ":(literal)" + relative
		case "tree":
			if glob == "" {
				pathspec = ":(literal)" + relative
			} else {
				pathspec = ":(glob)" + gitGlobPathLiteral.Replace(relative) + "/" + recursiveGitGlob(glob)
			}
		default:
			return nil, false, fmt.Errorf("source path %q is not a file or directory", relative)
		}
	} else if glob != "" {
		pathspec = ":(glob)" + recursiveGitGlob(glob)
	}
	patternFlag := "-F"
	if queryMode == "regex" {
		patternFlag = "-E"
	}
	cmdArgs := []string{"-C", repoDir, "grep", "--line-number", patternFlag}
	if contextLines > 0 {
		cmdArgs = append(cmdArgs, "-C", strconv.Itoa(contextLines))
	}
	cmdArgs = append(cmdArgs, "-e", query, ref, "--")
	if pathspec != "" {
		cmdArgs = append(cmdArgs, pathspec)
	}
	cmdArgs = append(cmdArgs, safety.SensitiveGitPathspecs()...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout cappedBuffer
	stdout.limit = 4 << 20
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, false, nil // no matches
		}
		return nil, false, fmt.Errorf("git grep: %s", strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	truncated := stdout.truncated || (limit > 0 && len(lines) > limit)
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, truncated, nil
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func (b *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	b.truncated = b.truncated || original > remaining
	if remaining <= 0 {
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
	}
	_, err := b.buffer.Write(value)
	return original, err
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
		if !e.IsDir() {
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
	resolved, err := paths.ResolveReadablePath(path)
	if err == nil {
		return resolved, nil
	}
	if stripped, ok := stripWorkspaceRootBase(paths, path); ok {
		if resolved, retryErr := paths.ResolveReadablePath(stripped); retryErr == nil {
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
