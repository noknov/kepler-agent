package codegraph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/llm"
	"github.com/noknov/slack-copilot-agent/internal/safety"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/gitcache"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
)

type Base struct {
	Paths   safety.WorkspacePolicy
	Timeout time.Duration
}

func (b Base) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 30 * time.Second
}

type OverviewTool struct{ Base }
type DependenciesTool struct{ Base }
type CallersTool struct{ Base }

func (OverviewTool) Parallel() bool     { return true }
func (DependenciesTool) Parallel() bool { return true }
func (CallersTool) Parallel() bool      { return true }

func (t OverviewTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-overview",
		"Build a lightweight code graph for a refreshed git branch snapshot and summarize packages, internal dependencies, and function counts. Use for architecture orientation before detailed code reads.",
		registry.ObjectSchema(nil, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum packages/edges to show, default 40 and max 120."},
		}),
	)
}

func (t OverviewTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Limit  int    `json:"limit"`
	}
	_ = json.Unmarshal(raw, &args)
	limit := boundedLimit(args.Limit, 40, 120)
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString(fmt.Sprintf("\npackages=%d files=%d funcs=%d internal_edges=%d\n\n", len(g.Packages), g.Files, g.Funcs, len(g.InternalEdges)))
	for _, pkg := range g.sortedPackages() {
		if limit == 0 {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s files=%d funcs=%d imports=%d imported_by=%d\n", pkg.Path, pkg.Files, pkg.Funcs, len(pkg.InternalImports), len(pkg.ImportedBy)))
		limit--
	}
	if len(g.InternalEdges) > 0 {
		b.WriteString("\ninternal dependencies:\n")
		for i, edge := range g.InternalEdges {
			if i >= boundedLimit(args.Limit, 40, 120) {
				b.WriteString("...[truncated]\n")
				break
			}
			b.WriteString(edge.From + " -> " + edge.To + "\n")
		}
	}
	return registry.Result{Content: b.String()}, nil
}

func (t DependenciesTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-dependencies",
		"Show internal imports and importers for one package in a refreshed git branch snapshot. Use to understand package coupling before reading files.",
		registry.ObjectSchema([]string{"package"}, map[string]any{
			"repo":    map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch":  map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"package": map[string]any{"type": "string", "description": "Package import path, directory, or package name fragment."},
		}),
	)
}

func (t DependenciesTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo    string `json:"repo"`
		Branch  string `json:"branch"`
		Package string `json:"package"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	pkg, err := g.findPackage(args.Package)
	if err != nil {
		return registry.Result{}, err
	}
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\npackage=" + pkg.Path + "\n")
	b.WriteString("files=" + strconv.Itoa(pkg.Files) + " funcs=" + strconv.Itoa(pkg.Funcs) + "\n\n")
	writeList(&b, "imports", pkg.InternalImports)
	writeList(&b, "imported_by", pkg.ImportedBy)
	return registry.Result{Content: b.String()}, nil
}

func (t CallersTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-callers",
		"Find simple Go call sites for a function or method name in a refreshed git branch snapshot. This is a static hint; read source ranges before making final behavior claims.",
		registry.ObjectSchema([]string{"symbol"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"symbol": map[string]any{"type": "string", "description": "Function or method name, for example FetchOrigin or Manager.Start."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum callers to show, default 50 and max 200."},
		}),
	)
}

func (t CallersTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	symbol := shortSymbol(args.Symbol)
	if symbol == "" {
		return registry.Result{}, fmt.Errorf("symbol is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	var hits []Call
	for _, call := range g.Calls {
		if call.Callee == symbol {
			hits = append(hits, call)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].File == hits[j].File {
			return hits[i].Line < hits[j].Line
		}
		return hits[i].File < hits[j].File
	})
	limit := boundedLimit(args.Limit, 50, 200)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\nsymbol=" + symbol + "\n\n")
	if len(hits) == 0 {
		b.WriteString("no callers found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, hit := range hits {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s\n", hit.File, hit.Line, hit.Caller))
	}
	return registry.Result{Content: b.String()}, nil
}

func (b Base) load(ctx context.Context, repoArg, branchArg string, rt registry.Runtime) (*Graph, error) {
	repo, err := b.repo(repoArg)
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(branchArg)
	fetchCtx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()
	fetchStatus := "origin_refs_current_or_recent"
	if branch != "" {
		if err := gitcache.FetchOriginFresh(fetchCtx, repo); err != nil {
			fetchStatus = "refresh_failed_using_cached_refs: " + err.Error()
		} else {
			fetchStatus = "origin_refs_refreshed"
		}
	} else if err := gitcache.FetchOrigin(fetchCtx, repo, gitcache.DefaultFetchTTL); err != nil {
		fetchStatus = "refresh_failed_using_cached_refs: " + err.Error()
	}
	if branch == "" {
		branch = defaultBranch(ctx, repo)
	}
	if !safeRef(branch) {
		return nil, fmt.Errorf("invalid branch %q", branch)
	}
	ref := "origin/" + branch
	commit, err := git(ctx, repo, b.timeout(), "rev-parse", ref)
	if err != nil {
		return nil, err
	}
	key := "codegraph\x00" + repo + "\x00" + strings.TrimSpace(commit)
	if rt.Cache != nil {
		if cached, ok := rt.Cache.Get(key); ok {
			if g, ok := cached.(*Graph); ok {
				return g, nil
			}
		}
	}
	g, err := buildGraph(ctx, repo, ref, strings.TrimSpace(commit), branch, fetchStatus, b.timeout())
	if err != nil {
		return nil, err
	}
	if rt.Cache != nil {
		rt.Cache.Set(key, g)
	}
	return g, nil
}

type Graph struct {
	Repo          string
	Branch        string
	Ref           string
	Commit        string
	FetchStatus   string
	Module        string
	Files         int
	Funcs         int
	Packages      map[string]*Package
	InternalEdges []Edge
	Calls         []Call
}

type Package struct {
	Path            string
	Name            string
	Dir             string
	Files           int
	Funcs           int
	InternalImports []string
	ImportedBy      []string
}

type Edge struct{ From, To string }

type Call struct {
	Caller string
	Callee string
	File   string
	Line   int
}

func buildGraph(ctx context.Context, repo, ref, commit, branch, fetchStatus string, timeout time.Duration) (*Graph, error) {
	filesOut, err := git(ctx, repo, timeout, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	module := modulePath(ctx, repo, ref, timeout)
	g := &Graph{
		Repo:        filepath.Base(repo),
		Branch:      branch,
		Ref:         ref,
		Commit:      commit,
		FetchStatus: fetchStatus,
		Module:      module,
		Packages:    map[string]*Package{},
	}
	importSets := map[string]map[string]bool{}
	importedBySets := map[string]map[string]bool{}
	edgeSet := map[string]bool{}
	for _, path := range strings.Split(strings.TrimSpace(filesOut), "\n") {
		if !includeGoFile(path) {
			continue
		}
		src, err := git(ctx, repo, timeout, "show", ref+":"+path)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			continue
		}
		pkgPath := packagePath(module, filepath.Dir(path))
		pkg := g.Packages[pkgPath]
		if pkg == nil {
			pkg = &Package{Path: pkgPath, Name: file.Name.Name, Dir: filepath.ToSlash(filepath.Dir(path))}
			g.Packages[pkgPath] = pkg
		}
		g.Files++
		pkg.Files++
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if module == "" || !strings.HasPrefix(importPath, module) {
				continue
			}
			if importSets[pkgPath] == nil {
				importSets[pkgPath] = map[string]bool{}
			}
			importSets[pkgPath][importPath] = true
			if importedBySets[importPath] == nil {
				importedBySets[importPath] = map[string]bool{}
			}
			importedBySets[importPath][pkgPath] = true
			edgeKey := pkgPath + "\x00" + importPath
			if !edgeSet[edgeKey] {
				edgeSet[edgeKey] = true
				g.InternalEdges = append(g.InternalEdges, Edge{From: pkgPath, To: importPath})
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			g.Funcs++
			pkg.Funcs++
			caller := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				caller = receiverName(fn.Recv.List[0].Type) + "." + caller
			}
			if fn.Body == nil {
				return false
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := callName(call.Fun)
				if callee != "" {
					pos := fset.Position(call.Pos())
					g.Calls = append(g.Calls, Call{Caller: caller, Callee: callee, File: filepath.ToSlash(path), Line: pos.Line})
				}
				return true
			})
			return false
		})
	}
	for path, pkg := range g.Packages {
		pkg.InternalImports = sortedSet(importSets[path])
		pkg.ImportedBy = sortedSet(importedBySets[path])
	}
	sort.Slice(g.InternalEdges, func(i, j int) bool {
		if g.InternalEdges[i].From == g.InternalEdges[j].From {
			return g.InternalEdges[i].To < g.InternalEdges[j].To
		}
		return g.InternalEdges[i].From < g.InternalEdges[j].From
	})
	return g, nil
}

func (g *Graph) header() string {
	return fmt.Sprintf("repo=%s\nbranch=%s\nref=%s\ncommit=%s\nfetch_status=%s\nmodule=%s\nworking_tree_changed=false\n", g.Repo, g.Branch, g.Ref, shortCommit(g.Commit), g.FetchStatus, g.Module)
}

func (g *Graph) sortedPackages() []*Package {
	pkgs := make([]*Package, 0, len(g.Packages))
	for _, pkg := range g.Packages {
		pkgs = append(pkgs, pkg)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Path < pkgs[j].Path })
	return pkgs
}

func (g *Graph) findPackage(query string) (*Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("package is required")
	}
	if pkg := g.Packages[query]; pkg != nil {
		return pkg, nil
	}
	var matches []*Package
	for _, pkg := range g.Packages {
		if pkg.Name == query || pkg.Dir == query || strings.HasSuffix(pkg.Path, "/"+query) || strings.Contains(pkg.Path, query) {
			matches = append(matches, pkg)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Path < matches[j].Path })
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("package %q not found", query)
	}
	names := make([]string, 0, len(matches))
	for _, pkg := range matches {
		names = append(names, pkg.Path)
	}
	return nil, fmt.Errorf("package %q is ambiguous: %s", query, strings.Join(names, ", "))
}

func (b Base) repo(path string) (string, error) {
	if strings.TrimSpace(path) == "" && len(b.Paths.Roots) > 0 {
		path = b.Paths.Roots[0]
	}
	resolved, err := b.Paths.Resolve(path)
	if err != nil {
		return "", err
	}
	if isGitRepo(resolved) {
		return resolved, nil
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return resolved, nil
	}
	var repos []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		sub := filepath.Join(resolved, entry.Name())
		if isGitRepo(sub) {
			repos = append(repos, sub)
		}
	}
	if len(repos) == 1 {
		return repos[0], nil
	}
	if len(repos) > 1 {
		names := make([]string, len(repos))
		for i, repo := range repos {
			names[i] = filepath.Base(repo)
		}
		return "", fmt.Errorf("workspace contains multiple repos; pass repo=%s", strings.Join(names, " | "))
	}
	return resolved, nil
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

func defaultBranch(ctx context.Context, repo string) string {
	if out, err := git(ctx, repo, 10*time.Second, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if branch := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); branch != "" {
			return branch
		}
	}
	for _, branch := range []string{"mt-main", "main", "master"} {
		if _, err := git(ctx, repo, 10*time.Second, "rev-parse", "--verify", "--quiet", "origin/"+branch); err == nil {
			return branch
		}
	}
	return "main"
}

func modulePath(ctx context.Context, repo, ref string, timeout time.Duration) string {
	out, err := git(ctx, repo, timeout, "show", ref+":go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

func git(ctx context.Context, repo string, timeout time.Duration, args ...string) (string, error) {
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

func includeGoFile(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "vendor" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func packagePath(module, dir string) string {
	dir = filepath.ToSlash(dir)
	if dir == "." {
		return module
	}
	if module == "" {
		return dir
	}
	return module + "/" + dir
}

func callName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	default:
		return ""
	}
}

func receiverName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	default:
		return "receiver"
	}
}

func shortSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if idx := strings.LastIndex(symbol, "."); idx >= 0 {
		return symbol[idx+1:]
	}
	return symbol
}

func safeRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" && !strings.Contains(ref, "..") && !strings.HasPrefix(ref, "-") && !strings.ContainsAny(ref, " \t\n\r~^:?*[\\")
}

func boundedLimit(value, def, max int) int {
	if value <= 0 {
		return def
	}
	if value > max {
		return max
	}
	return value
}

func sortedSet(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for value := range in {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func writeList(b *strings.Builder, label string, values []string) {
	b.WriteString(label + ":\n")
	if len(values) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, value := range values {
		b.WriteString("- " + value + "\n")
	}
}

func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
