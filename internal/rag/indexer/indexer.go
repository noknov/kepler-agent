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

	var changedFiles []changedFile
	if found && state.LastCommit != "" {
		changedFiles, err = gitDiffFiles(ctx, repoPath, state.LastCommit, currentCommit)
		if err != nil {
			log.Printf("rag/indexer: git diff failed for %s, falling back to full index: %v", repoPath, err)
			changedFiles = nil
			found = false
		}
	}

	if !found || changedFiles == nil {
		files, err := gitListFiles(ctx, repoPath, currentCommit)
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		changedFiles = make([]changedFile, 0, len(files))
		for _, file := range files {
			changedFiles = append(changedFiles, changedFile{Path: file})
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

	for _, file := range indexable {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if file.Deleted {
			if err := idx.Store.DeleteChunksForFile(ctx, repoPath, branch, file.Path); err != nil {
				log.Printf("rag/indexer: delete chunks for removed file %s: %v", file.Path, err)
			}
			continue
		}

		if err := idx.Store.DeleteChunksForFile(ctx, repoPath, branch, file.Path); err != nil {
			return nil, fmt.Errorf("delete old chunks for %s: %w", file.Path, err)
		}

		content, err := gitShowFile(ctx, repoPath, currentCommit, file.Path)
		if err != nil {
			log.Printf("rag/indexer: read %s at %s: %v", file.Path, currentCommit, err)
			continue
		}

		chunks, err := idx.Chunker.Chunk(file.Path, content)
		if err != nil {
			log.Printf("rag/indexer: chunk %s: %v", file.Path, err)
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

type changedFile struct {
	Path    string
	Deleted bool
}

func filterIndexable(files []changedFile) []changedFile {
	out := make([]changedFile, 0, len(files))
	for _, f := range files {
		if chunk.ShouldIndex(f.Path) {
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

func gitDiffFiles(ctx context.Context, repo, fromCommit, toCommit string) ([]changedFile, error) {
	out, err := gitRun(ctx, repo, "diff", "--name-status", "--diff-filter=ACDMR", fromCommit+".."+toCommit)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

func parseNameStatus(out string) []changedFile {
	lines := splitNonEmpty(out)
	files := make([]changedFile, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status := fields[0]
		path := fields[1]
		deleted := strings.HasPrefix(status, "D")
		if strings.HasPrefix(status, "R") && len(fields) >= 3 {
			files = append(files, changedFile{Path: fields[1], Deleted: true})
			path = fields[2]
		}
		files = append(files, changedFile{Path: path, Deleted: deleted})
	}
	return files
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
