package observability

import (
	"errors"
	"testing"
	"time"
)

func TestRecorderTracksRAGIndexAndSearchHealth(t *testing.T) {
	rec := NewRecorder()
	rec.RAGIndexSuccess("/repo", "main", "abcdef1234567890", 2, 10, 8, 2, 1, 25*time.Millisecond)
	rec.RAGSearch(3, true, nil)
	rec.RAGIndexError("/repo", "main", 10*time.Millisecond, errors.New("boom"))
	rec.RAGSearch(0, false, errors.New("query failed"))

	snap := rec.Snapshot()
	if snap.RAG.IndexRuns != 2 {
		t.Fatalf("IndexRuns = %d, want 2", snap.RAG.IndexRuns)
	}
	if snap.RAG.IndexErrors != 1 {
		t.Fatalf("IndexErrors = %d, want 1", snap.RAG.IndexErrors)
	}
	if snap.RAG.Searches != 2 || snap.RAG.SearchErrors != 1 || snap.RAG.SearchStaleHits != 1 {
		t.Fatalf("unexpected RAG search counters: %#v", snap.RAG)
	}
	state := snap.RAG.Indexes["/repo@main"]
	if state.LastCommit != "abcdef1234567890" {
		t.Fatalf("LastCommit = %q", state.LastCommit)
	}
	if state.LastChunksReused != 8 {
		t.Fatalf("LastChunksReused = %d, want 8", state.LastChunksReused)
	}
	if state.LastChunksSplitLarge != 2 {
		t.Fatalf("LastChunksSplitLarge = %d, want 2", state.LastChunksSplitLarge)
	}
	if state.LastChunksSkippedLarge != 1 {
		t.Fatalf("LastChunksSkippedLarge = %d, want 1", state.LastChunksSkippedLarge)
	}
	if state.LastError != "boom" {
		t.Fatalf("LastError = %q, want boom", state.LastError)
	}
}
