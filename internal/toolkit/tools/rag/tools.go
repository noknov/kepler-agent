package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	internrag "github.com/wati/oncall-agent/internal/rag"
	"github.com/wati/oncall-agent/internal/rag/store"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type SearchTool struct {
	Manager  *internrag.Manager
	Paths    safety.WorkspacePolicy
	Observer Observer
}

type Observer interface {
	RAGSearch(results int, stale bool, err error)
}

func (SearchTool) Parallel() bool { return true }

func (t SearchTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"rag-search",
		"Semantic code search using RAG. Use for questions like 'how does authentication work', "+
			"'where is error retry logic', or 'what handles webhook events'. "+
			"For exact symbol/string matching, prefer code-search or repo-search instead.",
		registry.ObjectSchema([]string{"query"}, map[string]any{
			"query":  map[string]any{"type": "string", "description": "Natural language or code-related query."},
			"repo":   map[string]any{"type": "string", "description": "Repository path under WORKSPACE_ROOTS. Defaults to first root."},
			"branch": map[string]any{"type": "string", "description": "Branch to search. Defaults to the repo's default branch."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum results. Defaults to 10, max 20."},
		}),
	)
}

func (t SearchTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Query  string `json:"query"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if strings.TrimSpace(args.Query) == "" {
		return registry.Result{}, fmt.Errorf("query is required")
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.Limit > 20 {
		args.Limit = 20
	}

	repo := args.Repo
	if repo == "" && len(t.Paths.Roots) > 0 {
		repo = t.Paths.Roots[0]
	}
	repoPath, err := t.Paths.Resolve(repo)
	if err != nil {
		return registry.Result{}, err
	}
	repoPath, _ = filepath.Abs(repoPath)

	branch := args.Branch
	if branch == "" {
		branch = defaultBranch(ctx, repoPath)
	}

	state, indexed, stateErr := t.Manager.GetIndexState(ctx, repoPath, branch)
	currentCommit := currentCommit(ctx, repoPath, branch)
	stale := indexed && currentCommit != "" && state.LastCommit != "" && state.LastCommit != currentCommit

	results, err := t.Manager.Search(ctx, args.Query, repoPath, branch, args.Limit)
	if t.Observer != nil {
		t.Observer.RAGSearch(len(results), stale, err)
	}
	if err != nil {
		return registry.Result{}, fmt.Errorf("rag search: %w", err)
	}

	if len(results) == 0 {
		var b strings.Builder
		b.WriteString("no matching code found")
		writeIndexSummary(&b, repoPath, branch, state, indexed, stateErr, currentCommit)
		return registry.Result{Content: b.String()}, nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d results for: %s\n\n", len(results), args.Query))
	writeIndexSummary(&b, repoPath, branch, state, indexed, stateErr, currentCommit)
	if stale {
		b.WriteString("warning: RAG index is stale for this branch; prefer exact repo/code search before making a final claim.\n\n")
	}
	for i, r := range results {
		b.WriteString(fmt.Sprintf("--- Result %d [score=%.3f source=%s] ---\n", i+1, r.Score, r.Source))
		if r.SymbolName != "" {
			b.WriteString(fmt.Sprintf("symbol: %s\n", r.SymbolName))
		}
		b.WriteString(fmt.Sprintf("file: %s", r.FilePath))
		if r.StartLine > 0 {
			b.WriteString(fmt.Sprintf(":%d-%d", r.StartLine, r.EndLine))
		}
		b.WriteString(fmt.Sprintf(" [%s]\n", r.Language))
		if r.CommitSHA != "" {
			b.WriteString(fmt.Sprintf("commit: %s\n", shortSHA(r.CommitSHA)))
		}
		content := r.Content
		if len(content) > 2000 {
			content = content[:2000] + "\n...[truncated]"
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	return registry.Result{Content: strings.TrimSpace(b.String())}, nil
}

func writeIndexSummary(b *strings.Builder, repoPath, branch string, state store.IndexState, indexed bool, stateErr error, currentCommit string) {
	b.WriteString(fmt.Sprintf("index: repo=%s branch=%s", repoPath, branch))
	if currentCommit != "" {
		b.WriteString(" current_commit=" + shortSHA(currentCommit))
	}
	if stateErr != nil {
		b.WriteString(" state_error=" + stateErr.Error())
	} else if indexed {
		b.WriteString(" indexed_commit=" + shortSHA(state.LastCommit))
	} else {
		b.WriteString(" indexed_commit=missing")
	}
	b.WriteString("\n\n")
}

func defaultBranch(ctx context.Context, repoPath string) string {
	for _, branch := range []string{"mt-main", "main", "master"} {
		if refExists(ctx, repoPath, "origin/"+branch) || refExists(ctx, repoPath, branch) {
			return branch
		}
	}
	return "main"
}

func currentCommit(ctx context.Context, repoPath, branch string) string {
	for _, ref := range []string{"origin/" + branch, branch} {
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "--no-optional-locks", "rev-parse", ref)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err == nil {
			return strings.TrimSpace(stdout.String())
		}
	}
	return ""
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func refExists(ctx context.Context, repoPath, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "--no-optional-locks", "rev-parse", "--verify", "--quiet", ref)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(stdout.String()) != ""
}
