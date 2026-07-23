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

	"github.com/noknov/slack-copilot-agent/internal/rag/chunk"
	"github.com/noknov/slack-copilot-agent/internal/rag/embedding"
	"github.com/noknov/slack-copilot-agent/internal/rag/store"
)

type Indexer struct {
	Store     store.Store
	Embedder  Embedder
	Chunker   *chunk.Dispatcher
	BatchSize int
}

type Embedder interface {
	EmbedBatched(ctx context.Context, texts []string, batchSize int) ([][]float32, error)
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
	Repo               string
	Branch             string
	CommitSHA          string
	FilesChanged       int
	ChunksAdded        int
	ChunksKept         int
	ChunksReused       int
	ChunksEmbedded     int
	ChunksSplitLarge   int
	ChunksSkippedLarge int
	Duration           time.Duration
}

const maxChunkContentBytes = 128 * 1024

// IndexRepo performs an incremental index of a single repo+branch.
// It detects changed files via git diff, re-chunks only those files,
// and only re-embeds chunks whose content actually changed.
func (idx *Indexer) IndexRepo(ctx context.Context, repoPath, branch string) (*IndexResult, error) {
	start := time.Now()
	repoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	ref, err := resolveBranchRef(ctx, repoPath, branch)
	if err != nil {
		return nil, err
	}

	currentCommit, err := gitRevParse(ctx, repoPath, ref)
	if err != nil {
		return nil, fmt.Errorf("rev-parse %s: %w", ref, err)
	}

	state, found, err := idx.Store.GetIndexState(ctx, repoPath, branch)
	if err != nil {
		return nil, fmt.Errorf("get index state: %w", err)
	}

	if found && state.LastCommit == currentCommit {
		return &IndexResult{
			Repo:      repoPath,
			Branch:    branch,
			CommitSHA: currentCommit,
			Duration:  time.Since(start),
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
		CommitSHA:    currentCommit,
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

		existing, err := idx.Store.GetChunksForFile(ctx, repoPath, branch, file.Path)
		if err != nil {
			return nil, fmt.Errorf("get old chunks for %s: %w", file.Path, err)
		}
		existingByID := chunksByID(existing)

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

		chunks = splitOversizedChunks(chunks, maxChunkContentBytes)
		for i := range chunks {
			chunks[i].RepoPath = repoPath
			chunks[i].Branch = branch
			chunks[i].CommitSHA = currentCommit
			chunks[i].ComputeContentHash()
			chunks[i].ComputeID()
		}
		disambiguateDuplicateIDs(chunks)

		for _, c := range chunks {
			if len(c.Content) > maxChunkContentBytes {
				result.ChunksSkippedLarge++
				log.Printf("rag/indexer: skip oversized chunk %s:%d-%d (%d bytes)", c.FilePath, c.StartLine, c.EndLine, len(c.Content))
				continue
			}
			if isSplitOversizedChunk(c) {
				result.ChunksSplitLarge++
			}
			rec := store.ChunkRecord{Chunk: c}
			if old, ok := existingByID[c.ID]; ok && old.ContentHash == c.ContentHash && len(old.Embedding) > 0 {
				rec.Embedding = old.Embedding
				result.ChunksReused++
			}
			idx := len(allChunks)
			allChunks = append(allChunks, rec)
			if len(rec.Embedding) == 0 {
				needEmbed = append(needEmbed, idx)
			}
		}

		if err := idx.Store.DeleteChunksForFile(ctx, repoPath, branch, file.Path); err != nil {
			return nil, fmt.Errorf("delete old chunks for %s: %w", file.Path, err)
		}
	}

	if len(needEmbed) > 0 {
		result.ChunksEmbedded = len(needEmbed)
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

const oversizedChunkContext = "oversized chunk split"

func splitOversizedChunks(chunks []chunk.Chunk, maxBytes int) []chunk.Chunk {
	if maxBytes <= 0 {
		return chunks
	}
	out := make([]chunk.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Content) <= maxBytes {
			out = append(out, c)
			continue
		}
		parts := splitChunkContent(c, maxBytes)
		out = append(out, parts...)
	}
	return out
}

func splitChunkContent(c chunk.Chunk, maxBytes int) []chunk.Chunk {
	lines := strings.Split(c.Content, "\n")
	var parts []chunk.Chunk
	startLine := c.StartLine
	var current []string
	currentBytes := 0

	flush := func(endLine int) {
		if len(current) == 0 {
			return
		}
		part := c
		part.StartLine = startLine
		part.EndLine = endLine
		part.Content = strings.Join(current, "\n")
		part.ContextPrefix = splitContextPrefix(c.ContextPrefix)
		parts = append(parts, part)
		current = nil
		currentBytes = 0
		startLine = endLine + 1
	}

	for i, line := range lines {
		lineNo := c.StartLine + i
		if len(line) > maxBytes {
			flush(lineNo - 1)
			parts = append(parts, splitLongLineChunk(c, line, lineNo, maxBytes)...)
			startLine = lineNo + 1
			continue
		}
		addBytes := len(line)
		if len(current) > 0 {
			addBytes++
		}
		if len(current) > 0 && currentBytes+addBytes > maxBytes {
			flush(lineNo - 1)
		}
		current = append(current, line)
		currentBytes += addBytes
	}
	flush(c.EndLine)
	return parts
}

func splitLongLineChunk(c chunk.Chunk, line string, lineNo, maxBytes int) []chunk.Chunk {
	var parts []chunk.Chunk
	var b strings.Builder
	for _, r := range line {
		rs := string(r)
		if b.Len() > 0 && b.Len()+len(rs) > maxBytes {
			part := c
			part.StartLine = lineNo
			part.EndLine = lineNo
			part.Content = b.String()
			part.ContextPrefix = splitContextPrefix(c.ContextPrefix)
			parts = append(parts, part)
			b.Reset()
		}
		b.WriteString(rs)
	}
	if b.Len() > 0 {
		part := c
		part.StartLine = lineNo
		part.EndLine = lineNo
		part.Content = b.String()
		part.ContextPrefix = splitContextPrefix(c.ContextPrefix)
		parts = append(parts, part)
	}
	return parts
}

func splitContextPrefix(existing string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return oversizedChunkContext
	}
	if strings.Contains(existing, oversizedChunkContext) {
		return existing
	}
	return existing + "\n" + oversizedChunkContext
}

func isSplitOversizedChunk(c chunk.Chunk) bool {
	return strings.Contains(c.ContextPrefix, oversizedChunkContext)
}

func disambiguateDuplicateIDs(chunks []chunk.Chunk) {
	seen := map[string]int{}
	for i := range chunks {
		id := chunks[i].ID
		seen[id]++
		if seen[id] > 1 {
			chunks[i].ID = fmt.Sprintf("%s-%d", id, seen[id])
		}
	}
}

func chunksByID(records []store.ChunkRecord) map[string]store.ChunkRecord {
	out := make(map[string]store.ChunkRecord, len(records))
	for _, r := range records {
		out[r.ID] = r
	}
	return out
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

func resolveBranchRef(ctx context.Context, repo, branch string) (string, error) {
	for _, ref := range []string{"origin/" + branch, branch} {
		if _, err := gitRevParse(ctx, repo, ref); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("branch %q does not exist as origin/%s or local %s", branch, branch, branch)
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
