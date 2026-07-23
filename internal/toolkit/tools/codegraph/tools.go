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
	"regexp"
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
type SymbolsTool struct{ Base }
type DefinitionTool struct{ Base }
type ReferencesTool struct{ Base }
type ImplementationsTool struct{ Base }
type CallersTool struct{ Base }
type CalleesTool struct{ Base }
type CallgraphTool struct{ Base }
type ImpactTool struct{ Base }

func (OverviewTool) Parallel() bool     { return true }
func (DependenciesTool) Parallel() bool { return true }
func (SymbolsTool) Parallel() bool      { return true }
func (DefinitionTool) Parallel() bool   { return true }
func (ReferencesTool) Parallel() bool   { return true }
func (ImplementationsTool) Parallel() bool {
	return true
}
func (CallersTool) Parallel() bool   { return true }
func (CalleesTool) Parallel() bool   { return true }
func (CallgraphTool) Parallel() bool { return true }
func (ImpactTool) Parallel() bool    { return true }

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

func (t SymbolsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-symbols",
		"Search static Go/C# symbols in a refreshed git branch snapshot without requiring language servers. Use when LSP symbols are unavailable or for branch-specific symbol discovery.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"query":  map[string]any{"type": "string", "description": "Symbol name or substring."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum symbols, default 50 and max 200."},
		}),
	)
}

func (t SymbolsTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return registry.Result{}, fmt.Errorf("query is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	matches := g.matchSymbols(query)
	limit := boundedLimit(args.Limit, 50, 200)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\nquery=" + query + "\n\n")
	if len(matches) == 0 {
		b.WriteString("no symbols found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, sym := range matches {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s %s package=%s\n", sym.File, sym.Line, sym.Kind, sym.FullName, sym.Package))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t DefinitionTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-definition",
		"Find static Go/C# symbol definitions by name in a refreshed git branch snapshot. This does not require gopls or csharp-ls.",
		registry.ObjectSchema([]string{"symbol"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"symbol": map[string]any{"type": "string", "description": "Symbol name, for example AddCommentRoutes or CommentController.GetPostList."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum definitions, default 20 and max 100."},
		}),
	)
}

func (t DefinitionTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	symbol := strings.TrimSpace(args.Symbol)
	if symbol == "" {
		return registry.Result{}, fmt.Errorf("symbol is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	matches := g.matchDefinitions(symbol)
	limit := boundedLimit(args.Limit, 20, 100)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\nsymbol=" + symbol + "\n\n")
	if len(matches) == 0 {
		b.WriteString("no definitions found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, sym := range matches {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s %s package=%s\n", sym.File, sym.Line, sym.Kind, sym.FullName, sym.Package))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t ReferencesTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-references",
		"Find static Go/C# references to a symbol name in a refreshed git branch snapshot. Use as a fallback when LSP references are unavailable.",
		registry.ObjectSchema([]string{"symbol"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"symbol": map[string]any{"type": "string", "description": "Symbol name, type name, function name, or method name."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum references, default 100 and max 300."},
		}),
	)
}

func (t ReferencesTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	symbol := strings.TrimSpace(args.Symbol)
	if symbol == "" {
		return registry.Result{}, fmt.Errorf("symbol is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	var hits []Reference
	for _, ref := range g.References {
		if symbolMatches(ref.Symbol, symbol) {
			hits = append(hits, ref)
		}
	}
	sortReferences(hits)
	limit := boundedLimit(args.Limit, 100, 300)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\nsymbol=" + symbol + "\n\n")
	if len(hits) == 0 {
		b.WriteString("no references found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, ref := range hits {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s context=%s\n", ref.File, ref.Line, ref.Symbol, ref.Context))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t ImplementationsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-implementations",
		"Find static Go interface implementers or C# interface/base implementations in a refreshed git branch snapshot. Use as a fallback when LSP implementation lookup is unavailable.",
		registry.ObjectSchema([]string{"symbol"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"symbol": map[string]any{"type": "string", "description": "Interface, base type, or type name."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum implementations, default 50 and max 200."},
		}),
	)
}

func (t ImplementationsTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	symbol := strings.TrimSpace(args.Symbol)
	if symbol == "" {
		return registry.Result{}, fmt.Errorf("symbol is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	hits := g.implementations(symbol)
	limit := boundedLimit(args.Limit, 50, 200)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\nsymbol=" + symbol + "\n\n")
	if len(hits) == 0 {
		b.WriteString("no implementations found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, sym := range hits {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s %s package=%s\n", sym.File, sym.Line, sym.Kind, sym.FullName, sym.Package))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t CallersTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-callers",
		"Find simple Go/C# call sites for a function or method name in a refreshed git branch snapshot. This is a static hint; read source ranges before making final behavior claims.",
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
		if symbolMatches(call.Callee, symbol) {
			hits = append(hits, call)
		}
	}
	sortCalls(hits)
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
		b.WriteString(fmt.Sprintf("%s:%d %s -> %s\n", hit.File, hit.Line, hit.Caller, hit.Callee))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t CalleesTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-callees",
		"Find simple Go/C# outgoing calls made by a function or method in a refreshed git branch snapshot. Use when LSP outgoing calls are unavailable.",
		registry.ObjectSchema([]string{"symbol"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"symbol": map[string]any{"type": "string", "description": "Function or method name, for example getPostList or CommentController.GetPostList."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum callees to show, default 80 and max 300."},
		}),
	)
}

func (t CalleesTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	symbol := strings.TrimSpace(args.Symbol)
	if symbol == "" {
		return registry.Result{}, fmt.Errorf("symbol is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	var hits []Call
	for _, call := range g.Calls {
		if symbolMatches(call.Caller, symbol) {
			hits = append(hits, call)
		}
	}
	sortCalls(hits)
	limit := boundedLimit(args.Limit, 80, 300)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\nsymbol=" + symbol + "\n\n")
	if len(hits) == 0 {
		b.WriteString("no callees found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, hit := range hits {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s -> %s\n", hit.File, hit.Line, hit.Caller, hit.Callee))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t CallgraphTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-callgraph",
		"List static Go/C# call edges in a refreshed git branch snapshot, optionally filtered by caller/callee/package/file substring.",
		registry.ObjectSchema(nil, map[string]any{
			"repo":    map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch":  map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"filter":  map[string]any{"type": "string", "description": "Optional substring matched against caller, callee, or file."},
			"package": map[string]any{"type": "string", "description": "Optional package/path substring."},
			"limit":   map[string]any{"type": "integer", "description": "Maximum edges, default 120 and max 500."},
		}),
	)
}

func (t CallgraphTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo    string `json:"repo"`
		Branch  string `json:"branch"`
		Filter  string `json:"filter"`
		Package string `json:"package"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	filter := strings.ToLower(strings.TrimSpace(args.Filter))
	pkgFilter := strings.ToLower(strings.TrimSpace(args.Package))
	var hits []Call
	for _, call := range g.Calls {
		if filter != "" && !strings.Contains(strings.ToLower(call.Caller+"\x00"+call.Callee+"\x00"+call.File), filter) {
			continue
		}
		if pkgFilter != "" && !strings.Contains(strings.ToLower(g.packageForFile(call.File)), pkgFilter) && !strings.Contains(strings.ToLower(call.File), pkgFilter) {
			continue
		}
		hits = append(hits, call)
	}
	sortCalls(hits)
	limit := boundedLimit(args.Limit, 120, 500)
	var b strings.Builder
	b.WriteString(g.header())
	if args.Filter != "" || args.Package != "" {
		b.WriteString(fmt.Sprintf("\nfilter=%s\npackage=%s\n", args.Filter, args.Package))
	}
	b.WriteString("\n")
	if len(hits) == 0 {
		b.WriteString("no call edges found\n")
		return registry.Result{Content: b.String()}, nil
	}
	for i, hit := range hits {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			break
		}
		b.WriteString(fmt.Sprintf("%s:%d %s -> %s\n", hit.File, hit.Line, hit.Caller, hit.Callee))
	}
	return registry.Result{Content: b.String()}, nil
}

func (t ImpactTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"codegraph-impact",
		"Estimate static impact for a Go/C# package or symbol in a refreshed git branch snapshot. Shows package importers and direct callers; read sources before final claims.",
		registry.ObjectSchema([]string{"target"}, map[string]any{
			"repo":   map[string]any{"type": "string", "description": "Repository path or workspace-relative repo name. Required when workspace has multiple repos."},
			"branch": map[string]any{"type": "string", "description": "Remote branch name. Omit for origin/HEAD, then mt-main/main/master fallback."},
			"target": map[string]any{"type": "string", "description": "Package path/name or function/method symbol."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum callers/importers to show, default 80 and max 300."},
		}),
	)
}

func (t ImpactTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Target string `json:"target"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	target := strings.TrimSpace(args.Target)
	if target == "" {
		return registry.Result{}, fmt.Errorf("target is required")
	}
	g, err := t.load(ctx, args.Repo, args.Branch, rt)
	if err != nil {
		return registry.Result{}, err
	}
	limit := boundedLimit(args.Limit, 80, 300)
	var b strings.Builder
	b.WriteString(g.header())
	b.WriteString("\ntarget=" + target + "\n\n")
	if pkg, err := g.findPackage(target); err == nil {
		b.WriteString("package_imported_by:\n")
		writeLimitedList(&b, pkg.ImportedBy, limit)
		b.WriteString("\n")
	}
	defs := g.matchDefinitions(target)
	if len(defs) > 0 {
		b.WriteString("definitions:\n")
		for i, sym := range defs {
			if i >= limit {
				b.WriteString("...[truncated]\n")
				break
			}
			b.WriteString(fmt.Sprintf("- %s:%d %s %s\n", sym.File, sym.Line, sym.Kind, sym.FullName))
		}
		b.WriteString("\ndirect_callers:\n")
		written := 0
		for _, call := range g.Calls {
			if !symbolMatches(call.Callee, target) {
				continue
			}
			if written >= limit {
				b.WriteString("...[truncated]\n")
				break
			}
			b.WriteString(fmt.Sprintf("- %s:%d %s\n", call.File, call.Line, call.Caller))
			written++
		}
		if written == 0 {
			b.WriteString("- none\n")
		}
	}
	if len(defs) == 0 {
		if _, err := g.findPackage(target); err != nil {
			b.WriteString("no package or symbol impact found\n")
		}
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
	Symbols       []Symbol
	References    []Reference
	Types         map[string]*TypeInfo
	Interfaces    map[string]*InterfaceInfo
	FilePackages  map[string]string
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

type Symbol struct {
	Name     string
	FullName string
	Kind     string
	Package  string
	File     string
	Line     int
}

type Reference struct {
	Symbol  string
	File    string
	Line    int
	Context string
}

type TypeInfo struct {
	Symbol
	Methods []string
	Bases   []string
}

type InterfaceInfo struct {
	Symbol
	Methods []string
}

func buildGraph(ctx context.Context, repo, ref, commit, branch, fetchStatus string, timeout time.Duration) (*Graph, error) {
	filesOut, err := git(ctx, repo, timeout, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	module := modulePath(ctx, repo, ref, timeout)
	g := &Graph{
		Repo:         filepath.Base(repo),
		Branch:       branch,
		Ref:          ref,
		Commit:       commit,
		FetchStatus:  fetchStatus,
		Module:       module,
		Packages:     map[string]*Package{},
		Types:        map[string]*TypeInfo{},
		Interfaces:   map[string]*InterfaceInfo{},
		FilePackages: map[string]string{},
	}
	importSets := map[string]map[string]bool{}
	importedBySets := map[string]map[string]bool{}
	edgeSet := map[string]bool{}
	for _, path := range strings.Split(strings.TrimSpace(filesOut), "\n") {
		if !includeSourceFile(path) {
			continue
		}
		src, err := git(ctx, repo, timeout, "show", ref+":"+path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(path, ".cs") {
			addCSharpFile(g, path, src)
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
		g.FilePackages[filepath.ToSlash(path)] = pkgPath
		g.Files++
		pkg.Files++
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						pos := fset.Position(s.Pos())
						kind := "go_type"
						var methods []string
						if iface, ok := s.Type.(*ast.InterfaceType); ok {
							kind = "go_interface"
							methods = interfaceMethods(iface)
						}
						sym := Symbol{
							Name:     s.Name.Name,
							FullName: s.Name.Name,
							Kind:     kind,
							Package:  pkgPath,
							File:     filepath.ToSlash(path),
							Line:     pos.Line,
						}
						g.Symbols = append(g.Symbols, sym)
						g.Types[s.Name.Name] = &TypeInfo{Symbol: sym}
						if kind == "go_interface" {
							g.Interfaces[s.Name.Name] = &InterfaceInfo{Symbol: sym, Methods: methods}
						}
					case *ast.ValueSpec:
						kind := "go_var"
						if d.Tok.String() == "const" {
							kind = "go_const"
						}
						for _, name := range s.Names {
							pos := fset.Position(name.Pos())
							g.Symbols = append(g.Symbols, Symbol{
								Name:     name.Name,
								FullName: name.Name,
								Kind:     kind,
								Package:  pkgPath,
								File:     filepath.ToSlash(path),
								Line:     pos.Line,
							})
						}
					}
				}
			case *ast.FuncDecl:
				name := d.Name.Name
				fullName := name
				receiver := ""
				if d.Recv != nil && len(d.Recv.List) > 0 {
					receiver = receiverName(d.Recv.List[0].Type)
					fullName = receiver + "." + name
				}
				pos := fset.Position(d.Pos())
				g.Symbols = append(g.Symbols, Symbol{
					Name:     name,
					FullName: fullName,
					Kind:     "go_func",
					Package:  pkgPath,
					File:     filepath.ToSlash(path),
					Line:     pos.Line,
				})
				if receiver != "" {
					info := g.Types[receiver]
					if info == nil {
						info = &TypeInfo{Symbol: Symbol{Name: receiver, FullName: receiver, Kind: "go_type", Package: pkgPath}}
						g.Types[receiver] = info
					}
					info.Methods = appendUnique(info.Methods, name)
				}
			}
		}
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
				if ident, ok := n.(*ast.Ident); ok && ident.Name != "_" {
					pos := fset.Position(ident.Pos())
					g.References = append(g.References, Reference{
						Symbol:  ident.Name,
						File:    filepath.ToSlash(path),
						Line:    pos.Line,
						Context: caller,
					})
				}
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

var (
	csNamespaceRe = regexp.MustCompile(`^\s*namespace\s+([A-Za-z_][A-Za-z0-9_.]*)`)
	csTypeRe      = regexp.MustCompile(`\b(class|interface|struct|record|enum)\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*:\s*([A-Za-z0-9_<>,\s.]+))?`)
	csMethodRe    = regexp.MustCompile(`\b(?:public|private|protected|internal|static|async|virtual|override|sealed|partial|extern|unsafe|new|\s)+[A-Za-z_][A-Za-z0-9_<>,\[\].?]*\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	callRe        = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

func addCSharpFile(g *Graph, path, src string) {
	lines := strings.Split(src, "\n")
	namespace := ""
	currentType := ""
	pkgPath := filepath.ToSlash(filepath.Dir(path))
	g.Files++
	g.FilePackages[filepath.ToSlash(path)] = pkgPath
	for i, line := range lines {
		lineNo := i + 1
		if match := csNamespaceRe.FindStringSubmatch(line); match != nil {
			namespace = match[1]
			if namespace != "" {
				pkgPath = namespace
			}
			pkg := ensurePackage(g, pkgPath, namespace, filepath.Dir(path))
			if pkg.Files == 0 {
				pkg.Files = 1
			}
		}
		if match := csTypeRe.FindStringSubmatch(line); match != nil {
			currentType = match[2]
			bases := splitCSharpBases("")
			if len(match) > 3 {
				bases = splitCSharpBases(match[3])
			}
			pkg := ensurePackage(g, pkgPath, namespace, filepath.Dir(path))
			sym := Symbol{
				Name:     currentType,
				FullName: qualify(namespace, currentType),
				Kind:     "cs_" + match[1],
				Package:  pkgPath,
				File:     filepath.ToSlash(path),
				Line:     lineNo,
			}
			g.Symbols = append(g.Symbols, sym)
			g.Types[currentType] = &TypeInfo{Symbol: sym, Bases: bases}
			if match[1] == "interface" {
				g.Interfaces[currentType] = &InterfaceInfo{Symbol: sym}
			}
			pkg.Funcs += 0
		}
	}
	pkg := ensurePackage(g, pkgPath, namespace, filepath.Dir(path))
	if pkg.Files == 0 {
		pkg.Files = 1
	}
	for i := 0; i < len(lines); i++ {
		match := csMethodRe.FindStringSubmatch(lines[i])
		if match == nil || isControlKeyword(match[1]) {
			continue
		}
		name := match[1]
		fullName := name
		if currentType != "" {
			fullName = currentType + "." + name
		}
		g.Funcs++
		pkg.Funcs++
		g.Symbols = append(g.Symbols, Symbol{
			Name:     name,
			FullName: fullName,
			Kind:     "cs_method",
			Package:  pkgPath,
			File:     filepath.ToSlash(path),
			Line:     i + 1,
		})
		if currentType != "" {
			info := g.Types[currentType]
			if info == nil {
				info = &TypeInfo{Symbol: Symbol{Name: currentType, FullName: currentType, Kind: "cs_type", Package: pkgPath}}
				g.Types[currentType] = info
			}
			info.Methods = appendUnique(info.Methods, name)
		}
		for j := i; j < len(lines); j++ {
			for _, call := range callRe.FindAllStringSubmatch(lines[j], -1) {
				callee := call[1]
				if callee == name || isControlKeyword(callee) {
					continue
				}
				g.Calls = append(g.Calls, Call{Caller: fullName, Callee: callee, File: filepath.ToSlash(path), Line: j + 1})
				g.References = append(g.References, Reference{Symbol: callee, File: filepath.ToSlash(path), Line: j + 1, Context: fullName})
			}
			if j > i && strings.Contains(lines[j], "}") {
				break
			}
		}
	}
}

func ensurePackage(g *Graph, path, name, dir string) *Package {
	if path == "." || path == "" {
		path = filepath.ToSlash(dir)
	}
	pkg := g.Packages[path]
	if pkg == nil {
		pkg = &Package{Path: path, Name: name, Dir: filepath.ToSlash(dir)}
		g.Packages[path] = pkg
	}
	return pkg
}

func includeSourceFile(path string) bool {
	if strings.HasSuffix(path, "_test.go") {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "vendor" || part == "bin" || part == "obj" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".cs")
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

func interfaceMethods(iface *ast.InterfaceType) []string {
	if iface == nil || iface.Methods == nil {
		return nil
	}
	var methods []string
	for _, field := range iface.Methods.List {
		for _, name := range field.Names {
			methods = appendUnique(methods, name.Name)
		}
	}
	sort.Strings(methods)
	return methods
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func shortSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if idx := strings.LastIndex(symbol, "."); idx >= 0 {
		return symbol[idx+1:]
	}
	return symbol
}

func symbolMatches(candidate, query string) bool {
	candidate = strings.TrimSpace(candidate)
	query = strings.TrimSpace(query)
	if candidate == "" || query == "" {
		return false
	}
	if candidate == query || strings.EqualFold(candidate, query) {
		return true
	}
	short := shortSymbol(query)
	return candidate == short || strings.HasSuffix(candidate, "."+short)
}

func (g *Graph) matchSymbols(query string) []Symbol {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	var matches []Symbol
	for _, sym := range g.Symbols {
		if strings.Contains(strings.ToLower(sym.Name), queryLower) ||
			strings.Contains(strings.ToLower(sym.FullName), queryLower) ||
			strings.Contains(strings.ToLower(sym.Package), queryLower) {
			matches = append(matches, sym)
		}
	}
	sortSymbols(matches)
	return matches
}

func (g *Graph) matchDefinitions(query string) []Symbol {
	var exact []Symbol
	var fuzzy []Symbol
	for _, sym := range g.Symbols {
		if symbolMatches(sym.FullName, query) || symbolMatches(sym.Name, query) {
			exact = append(exact, sym)
			continue
		}
		if strings.Contains(strings.ToLower(sym.FullName), strings.ToLower(query)) {
			fuzzy = append(fuzzy, sym)
		}
	}
	sortSymbols(exact)
	sortSymbols(fuzzy)
	return append(exact, fuzzy...)
}

func (g *Graph) implementations(query string) []Symbol {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	short := shortSymbol(query)
	var hits []Symbol
	if iface := g.Interfaces[short]; iface != nil && len(iface.Methods) > 0 {
		for name, typ := range g.Types {
			if name == short {
				continue
			}
			if hasAllMethods(typ.Methods, iface.Methods) {
				hits = append(hits, typ.Symbol)
			}
		}
	} else {
		for _, typ := range g.Types {
			for _, base := range typ.Bases {
				if symbolMatches(base, query) {
					hits = append(hits, typ.Symbol)
					break
				}
			}
		}
	}
	sortSymbols(hits)
	return hits
}

func hasAllMethods(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	set := map[string]bool{}
	for _, method := range have {
		set[method] = true
	}
	for _, method := range want {
		if !set[method] {
			return false
		}
	}
	return true
}

func (g *Graph) packageForFile(file string) string {
	if g.FilePackages == nil {
		return ""
	}
	if pkg := g.FilePackages[filepath.ToSlash(file)]; pkg != "" {
		return pkg
	}
	return ""
}

func sortSymbols(symbols []Symbol) {
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].File == symbols[j].File {
			if symbols[i].Line == symbols[j].Line {
				return symbols[i].FullName < symbols[j].FullName
			}
			return symbols[i].Line < symbols[j].Line
		}
		return symbols[i].File < symbols[j].File
	})
}

func sortCalls(calls []Call) {
	sort.Slice(calls, func(i, j int) bool {
		if calls[i].File == calls[j].File {
			if calls[i].Line == calls[j].Line {
				return calls[i].Caller < calls[j].Caller
			}
			return calls[i].Line < calls[j].Line
		}
		return calls[i].File < calls[j].File
	})
}

func sortReferences(refs []Reference) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].File == refs[j].File {
			if refs[i].Line == refs[j].Line {
				return refs[i].Symbol < refs[j].Symbol
			}
			return refs[i].Line < refs[j].Line
		}
		return refs[i].File < refs[j].File
	})
}

func qualify(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

func isControlKeyword(name string) bool {
	switch name {
	case "if", "for", "foreach", "while", "switch", "catch", "using", "lock", "return", "new", "nameof", "typeof", "sizeof", "default":
		return true
	default:
		return false
	}
}

func splitCSharpBases(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.IndexAny(part, "< "); idx >= 0 {
			part = part[:idx]
		}
		out = append(out, part)
	}
	return out
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

func writeLimitedList(b *strings.Builder, values []string, limit int) {
	if len(values) == 0 {
		b.WriteString("- none\n")
		return
	}
	for i, value := range values {
		if i >= limit {
			b.WriteString("...[truncated]\n")
			return
		}
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
