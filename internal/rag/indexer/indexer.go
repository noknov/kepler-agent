package indexer

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/rag/chunk"
	"github.com/wati/oncall-agent/internal/rag/embedding"
	"github.com/wati/oncall-agent/internal/rag/store"
)

type Indexer struct {
	Store     store.Store
	Embedder  *embedding.Client
	Chunker   *chunk.Dispatcher
	BatchSize int
}

func New(s store.Store, emb *embedding.Client) *Indexer {
	return &Indexer{
		Store:     s,
		Embedder:  emb,
		Chunker:   chunk.NewDispatcher(),
		BatchSize: 64,
	}
}

type IndexResult struct {
	Repo         string
	Branch       string
	FilesChanged int
	ChunksAdded  int
	ChunksKept   int
	Duration     time.Duration
}

// IndexRepo performs an incremental index of a single repo+branch.
// It detects changed files via git diff, re-chunks only those files,
// and only re-embeds chunks whose content actually changed.
func (idx *Indexer) IndexRepo(ctx context.Context, repoPath, branch string) (*IndexResult, error) {
	start := time.Now()
	repoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	currentCommit, err := gitRevParse(ctx, repoPath, "origin/"+branch)
	if err != nil {
		return nil, fmt.Errorf("rev-parse origin/%s: %w", branch, err)
	}

	state, found, err := idx.Store.GetIndexState(ctx, repoPath, branch)
	if err != nil {
		return nil, fmt.Errorf("get index state: %w", err)
	}

	if found && state.LastCommit == currentCommit {
		return &IndexResult{
			Repo:     repoPath,
			Branch:   branch,
			Duration: time.Since(start),
		}, nil
	}

	var changedFiles []string
	if found && state.LastCommit != "" {
		changedFiles, err = gitDiffFiles(ctx, repoPath, state.LastCommit, currentCommit)
		if err != nil {
			log.Printf("rag/indexer: git diff failed for %s, falling back to full index: %v", repoPath, err)
			changedFiles = nil
			found = false
		}
	}

	if !found || changedFiles == nil {
		changedFiles, err = gitListFiles(ctx, repoPath, currentCommit)
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
	}

	indexable := filterIndexable(changedFiles)
	result := &IndexResult{
		Repo:         repoPath,
		Branch:       branch,
		FilesChanged: len(indexable),
	}

	var allChunks []store.ChunkRecord
	var needEmbed []int

	for _, filePath := range indexable {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		content, err := gitShowFile(ctx, repoPath, currentCommit, filePath)
		if err != nil {
			if err := idx.Store.DeleteChunksForFile(ctx, repoPath, branch, filePath); err != nil {
				log.Printf("rag/indexer: delete chunks for removed file %s: %v", filePath, err)
			}
			continue
		}

		chunks, err := idx.Chunker.Chunk(filePath, content)
		if err != nil {
			log.Printf("rag/indexer: chunk %s: %v", filePath, err)
			continue
		}

		for i := range chunks {
			chunks[i].RepoPath = repoPath
			chunks[i].Branch = branch
			chunks[i].CommitSHA = currentCommit
			chunks[i].ComputeContentHash()
			chunks[i].ComputeID()
		}

		for _, c := range chunks {
			rec := store.ChunkRecord{Chunk: c}
			idx := len(allChunks)
			allChunks = append(allChunks, rec)
			needEmbed = append(needEmbed, idx)
		}
	}

	if len(needEmbed) > 0 {
		texts := make([]string, len(needEmbed))
		for i, ci := range needEmbed {
			c := &allChunks[ci]
			texts[i] = buildEmbeddingText(c)
		}

		embeddings, err := idx.Embedder.EmbedBatched(ctx, texts, idx.BatchSize)
		if err != nil {
			return nil, fmt.Errorf("embed chunks: %w", err)
		}

		for i, ci := range needEmbed {
			if i < len(embeddings) && embeddings[i] != nil {
				allChunks[ci].Embedding = embeddings[i]
			}
		}
	}

	if err := idx.Store.UpsertChunks(ctx, allChunks); err != nil {
		return nil, fmt.Errorf("upsert chunks: %w", err)
	}

	result.ChunksAdded = len(allChunks)

	if err := idx.Store.SetIndexState(ctx, store.IndexState{
		RepoPath:   repoPath,
		Branch:     branch,
		LastCommit: currentCommit,
	}); err != nil {
		return nil, fmt.Errorf("set index state: %w", err)
	}

	result.Duration = time.Since(start)
	return result, nil
}

func buildEmbeddingText(c *store.ChunkRecord) string {
	var b strings.Builder
	if c.FilePath != "" {
		b.WriteString("file: " + c.FilePath + "\n")
	}
	if c.SymbolName != "" {
		b.WriteString("symbol: " + c.SymbolName + "\n")
	}
	if c.Package != "" {
		b.WriteString("package: " + c.Package + "\n")
	}
	if c.ContextPrefix != "" {
		b.WriteString(c.ContextPrefix + "\n\n")
	}
	b.WriteString(c.Content)
	return b.String()
}

func filterIndexable(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if chunk.ShouldIndex(f) {
			out = append(out, f)
		}
	}
	return out
}

func gitRevParse(ctx context.Context, repo, ref string) (string, error) {
	out, err := gitRun(ctx, repo, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitDiffFiles(ctx context.Context, repo, fromCommit, toCommit string) ([]string, error) {
	out, err := gitRun(ctx, repo, "diff", "--name-only", "--diff-filter=ACMR", fromCommit+".."+toCommit)
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(out), nil
}

func gitListFiles(ctx context.Context, repo, commit string) ([]string, error) {
	out, err := gitRun(ctx, repo, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(out), nil
}

func gitShowFile(ctx context.Context, repo, commit, path string) (string, error) {
	return gitRun(ctx, repo, "show", commit+":"+path)
}

func gitRun(ctx context.Context, repo string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repo, "--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func splitNonEmpty(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
