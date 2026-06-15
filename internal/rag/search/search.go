package search

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/rag/embedding"
	"github.com/wati/oncall-agent/internal/rag/store"
)

type Engine struct {
	Store    store.Store
	Embedder *embedding.Client
	Timeout  time.Duration
}

type Result struct {
	FilePath   string
	StartLine  int
	EndLine    int
	SymbolName string
	Language   string
	Content    string
	CommitSHA  string
	Score      float64
	Source     string
}

type Query struct {
	Text     string
	RepoPath string
	Branch   string
	Limit    int
}

// Search performs a hybrid search combining vector similarity, full-text,
// and optionally grep results.
func (e *Engine) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Limit <= 0 {
		q.Limit = 10
	}

	queryEmbedding, err := e.Embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(queryEmbedding) == 0 || len(queryEmbedding[0]) == 0 {
		return nil, fmt.Errorf("empty embedding for query")
	}

	hybridResults, err := e.Store.SearchHybrid(ctx, queryEmbedding[0], q.Text, q.RepoPath, q.Branch, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}

	grepResults := e.grepSearch(ctx, q)

	return e.mergeResults(hybridResults, grepResults, q.Limit), nil
}

// SearchSemanticOnly performs vector-only search (no grep).
func (e *Engine) SearchSemanticOnly(ctx context.Context, q Query) ([]Result, error) {
	if q.Limit <= 0 {
		q.Limit = 10
	}

	queryEmbedding, err := e.Embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(queryEmbedding) == 0 || len(queryEmbedding[0]) == 0 {
		return nil, fmt.Errorf("empty embedding for query")
	}

	results, err := e.Store.SearchSemantic(ctx, queryEmbedding[0], q.RepoPath, q.Branch, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}

	out := make([]Result, len(results))
	for i, r := range results {
		out[i] = storeToResult(r)
	}
	return out, nil
}

func (e *Engine) grepSearch(ctx context.Context, q Query) []Result {
	if q.RepoPath == "" {
		return nil
	}

	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	words := strings.Fields(q.Text)
	if len(words) == 0 {
		return nil
	}

	// Use the most specific-looking word for grep
	grepTerm := longestWord(words)
	if len(grepTerm) < 3 {
		return nil
	}

	ref := resolveBranchRef(ctx, q.RepoPath, q.Branch)
	if ref == "" {
		return nil
	}
	cmdArgs := []string{"-C", q.RepoPath, "--no-optional-locks",
		"grep", "-n", "--no-color", "-I", "-i",
		"-e", grepTerm, ref, "--"}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	limit := 20
	if len(lines) > limit {
		lines = lines[:limit]
	}

	var results []Result
	seen := make(map[string]bool)
	for _, line := range lines {
		line = strings.TrimPrefix(line, ref+":")
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		file := parts[0]
		if seen[file] {
			continue
		}
		seen[file] = true
		results = append(results, Result{
			FilePath:  file,
			Content:   strings.TrimSpace(parts[1]),
			Score:     0.1,
			Source:    "grep",
			CommitSHA: resolveCommit(ctx, q.RepoPath, ref),
		})
	}
	return results
}

func (e *Engine) mergeResults(hybrid []store.SearchResult, grep []Result, limit int) []Result {
	seen := make(map[string]bool)
	var merged []Result

	for _, r := range hybrid {
		key := r.FilePath + ":" + fmt.Sprintf("%d", r.StartLine)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, storeToResult(r))
	}

	for _, r := range grep {
		key := r.FilePath + ":grep"
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, existing := range merged {
			if existing.FilePath == r.FilePath {
				goto skip
			}
		}
		merged = append(merged, r)
	skip:
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func storeToResult(r store.SearchResult) Result {
	return Result{
		FilePath:   r.FilePath,
		StartLine:  r.StartLine,
		EndLine:    r.EndLine,
		SymbolName: r.SymbolName,
		Language:   r.Language,
		Content:    r.Content,
		CommitSHA:  r.CommitSHA,
		Score:      r.Score,
		Source:     r.Source,
	}
}

func longestWord(words []string) string {
	best := ""
	for _, w := range words {
		if len(w) > len(best) {
			best = w
		}
	}
	return best
}

func resolveBranchRef(ctx context.Context, repoPath, branch string) string {
	for _, ref := range []string{"origin/" + branch, branch} {
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "--no-optional-locks", "rev-parse", "--verify", "--quiet", ref)
		if err := cmd.Run(); err == nil {
			return ref
		}
	}
	return ""
}

func resolveCommit(ctx context.Context, repoPath, ref string) string {
	if ref == "" {
		return ""
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "--no-optional-locks", "rev-parse", ref)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}
