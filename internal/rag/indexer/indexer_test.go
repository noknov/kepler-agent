package indexer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/rag/chunk"
	"github.com/wati/oncall-agent/internal/rag/store"
)

func TestParseNameStatus(t *testing.T) {
	files := parseNameStatus("M\tcmd/main.go\nD\told.go\nR100\tbefore.go\tafter.go\n")
	if len(files) != 4 {
		t.Fatalf("expected 4 changed file entries, got %d: %#v", len(files), files)
	}

	check := func(i int, path string, deleted bool) {
		t.Helper()
		if files[i].Path != path || files[i].Deleted != deleted {
			t.Fatalf("files[%d] = %#v, want path=%q deleted=%v", i, files[i], path, deleted)
		}
	}

	check(0, "cmd/main.go", false)
	check(1, "old.go", true)
	check(2, "before.go", true)
	check(3, "after.go", false)
}

func TestFilterIndexableKeepsDeletedSourceFiles(t *testing.T) {
	files := filterIndexable([]changedFile{
		{Path: "old.go", Deleted: true},
		{Path: "image.png", Deleted: true},
		{Path: "README.md"},
	})

	if len(files) != 2 {
		t.Fatalf("expected 2 indexable files, got %d: %#v", len(files), files)
	}
	if files[0].Path != "old.go" || !files[0].Deleted {
		t.Fatalf("first file = %#v, want deleted old.go", files[0])
	}
	if files[1].Path != "README.md" || files[1].Deleted {
		t.Fatalf("second file = %#v, want live README.md", files[1])
	}
}

func TestResolveBranchRefFallsBackToLocalBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")

	ref, err := resolveBranchRef(context.Background(), repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "main" {
		t.Fatalf("ref = %q, want main", ref)
	}
}

func TestIndexRepoReusesExistingChunkEmbeddings(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	writeFile(t, repo, "main.go", `package main

func Stable() string {
	return "stable"
}

func Changed() string {
	return "old"
}
`)
	runGit(t, repo, "add", "main.go")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	initialCommit, err := gitRevParse(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}

	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	initialContent, err := gitShowFile(ctx, repo, initialCommit, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := chunk.NewDispatcher()
	initialChunks, err := dispatcher.Chunk("main.go", initialContent)
	if err != nil {
		t.Fatal(err)
	}
	existing := make([]store.ChunkRecord, 0, len(initialChunks))
	for i := range initialChunks {
		initialChunks[i].RepoPath = absRepo
		initialChunks[i].Branch = "main"
		initialChunks[i].CommitSHA = initialCommit
		initialChunks[i].ComputeContentHash()
		initialChunks[i].ComputeID()
		existing = append(existing, store.ChunkRecord{
			Chunk:     initialChunks[i],
			Embedding: []float32{1, 2, 3},
		})
	}

	writeFile(t, repo, "main.go", `package main

func Stable() string {
	return "stable"
}

func Changed() string {
	return "new"
}
`)
	runGit(t, repo, "add", "main.go")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "update changed")

	st := &memoryStore{
		state: store.IndexState{RepoPath: absRepo, Branch: "main", LastCommit: initialCommit},
		files: map[string][]store.ChunkRecord{
			"main.go": existing,
		},
	}
	emb := &recordingEmbedder{}
	idx := &Indexer{
		Store:     st,
		Embedder:  emb,
		Chunker:   chunk.NewDispatcher(),
		BatchSize: 64,
	}
	result, err := idx.IndexRepo(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesChanged != 1 {
		t.Fatalf("FilesChanged = %d, want 1", result.FilesChanged)
	}
	if emb.calls != 1 {
		t.Fatalf("embedding calls = %d, want 1", emb.calls)
	}
	if emb.inputs != 1 {
		t.Fatalf("embedding inputs = %d, want only the changed chunk", emb.inputs)
	}
	if len(st.upserts) == 0 {
		t.Fatal("expected upserted chunks")
	}
}

func TestIndexRepoSplitsOversizedChunks(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	writeFile(t, repo, "big.go", "package main\nfunc broken(\n"+strings.Repeat("x", maxChunkContentBytes+1))
	runGit(t, repo, "add", "big.go")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "big")

	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	st := &memoryStore{
		state: store.IndexState{RepoPath: absRepo, Branch: "main", LastCommit: "old"},
		files: map[string][]store.ChunkRecord{},
	}
	emb := &recordingEmbedder{}
	idx := &Indexer{
		Store:     st,
		Embedder:  emb,
		Chunker:   chunk.NewDispatcher(),
		BatchSize: 64,
	}

	result, err := idx.IndexRepo(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunksSplitLarge == 0 {
		t.Fatal("expected oversized chunk to be split")
	}
	if result.ChunksSkippedLarge != 0 {
		t.Fatalf("ChunksSkippedLarge = %d, want 0", result.ChunksSkippedLarge)
	}
	if emb.calls == 0 || emb.inputs == 0 {
		t.Fatalf("split chunks should be embedded, calls=%d inputs=%d", emb.calls, emb.inputs)
	}
	if len(st.upserts) == 0 {
		t.Fatal("expected split chunks to be upserted")
	}
	for _, rec := range st.upserts {
		if len(rec.Content) > maxChunkContentBytes {
			t.Fatalf("upserted oversized chunk with %d bytes", len(rec.Content))
		}
	}
}

func TestLoopSkipsIndexWhenFetchFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	writeFile(t, repo, "README.md", "# test\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	runGit(t, repo, "remote", "add", "origin", filepath.Join(root, "missing-remote.git"))

	st := &memoryStore{files: map[string][]store.ChunkRecord{}}
	obs := &recordingObserver{}
	loop := &Loop{
		Indexer: &Indexer{
			Store:     st,
			Embedder:  &recordingEmbedder{},
			Chunker:   chunk.NewDispatcher(),
			BatchSize: 64,
		},
		Roots:    []string{root},
		Observer: obs,
	}

	loop.indexAll(ctx)

	if obs.successes != 0 {
		t.Fatalf("successes = %d, want 0", obs.successes)
	}
	if obs.errors != 1 {
		t.Fatalf("errors = %d, want 1", obs.errors)
	}
	if st.state.LastCommit != "" {
		t.Fatalf("LastCommit = %q, want empty because indexing should be skipped", st.state.LastCommit)
	}
}

func TestIsTransientGitFetchError(t *testing.T) {
	if !isTransientGitFetchError(errors.New("git fetch --prune --no-write-fetch-head origin: fatal: early EOF")) {
		t.Fatal("early EOF should be treated as transient")
	}
	if !isTransientGitFetchError(errors.New("fatal: the remote end hung up unexpectedly")) {
		t.Fatal("remote disconnect should be treated as transient")
	}
	if isTransientGitFetchError(errors.New("fatal: Authentication failed for origin")) {
		t.Fatal("authentication failures should not be treated as transient")
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	fullPath := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type memoryStore struct {
	mu      sync.Mutex
	state   store.IndexState
	files   map[string][]store.ChunkRecord
	upserts []store.ChunkRecord
}

func (s *memoryStore) Migrate(context.Context) error { return nil }

func (s *memoryStore) UpsertChunks(_ context.Context, records []store.ChunkRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, records...)
	return nil
}

func (s *memoryStore) GetChunksForFile(_ context.Context, _, _, filePath string) ([]store.ChunkRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.files[filePath]
	out := make([]store.ChunkRecord, len(records))
	copy(out, records)
	return out, nil
}

func (s *memoryStore) DeleteChunksForFile(_ context.Context, _, _, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, filePath)
	return nil
}

func (s *memoryStore) DeleteStaleChunks(context.Context, string, string, []string) error {
	return nil
}

func (s *memoryStore) GetIndexState(context.Context, string, string) (store.IndexState, bool, error) {
	return s.state, true, nil
}

func (s *memoryStore) SetIndexState(_ context.Context, state store.IndexState) error {
	s.state = state
	return nil
}

func (s *memoryStore) SearchSemantic(context.Context, []float32, string, string, int) ([]store.SearchResult, error) {
	return nil, nil
}

func (s *memoryStore) SearchFullText(context.Context, string, string, string, int) ([]store.SearchResult, error) {
	return nil, nil
}

func (s *memoryStore) SearchHybrid(context.Context, []float32, string, string, string, int) ([]store.SearchResult, error) {
	return nil, nil
}

func (s *memoryStore) Close() error { return nil }

type recordingEmbedder struct {
	calls  int
	inputs int
}

func (e *recordingEmbedder) EmbedBatched(_ context.Context, texts []string, _ int) ([][]float32, error) {
	e.calls++
	e.inputs += len(texts)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{9, 9, 9}
	}
	return out, nil
}

type recordingObserver struct {
	successes int
	errors    int
}

func (o *recordingObserver) RAGIndexSuccess(string, string, string, int, int, int, int, int, time.Duration) {
	o.successes++
}

func (o *recordingObserver) RAGIndexError(string, string, time.Duration, error) {
	o.errors++
}
