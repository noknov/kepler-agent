package store

import (
	"context"

	"github.com/wati/oncall-agent/internal/rag/chunk"
)

type ChunkRecord struct {
	chunk.Chunk
	Embedding []float32
}

type IndexState struct {
	RepoPath   string
	Branch     string
	LastCommit string
}

type SearchResult struct {
	ChunkRecord
	Score      float64
	Source     string // "semantic", "fulltext", or "combined"
	Highlights []string
}

type Store interface {
	Migrate(ctx context.Context) error

	UpsertChunks(ctx context.Context, records []ChunkRecord) error
	DeleteChunksForFile(ctx context.Context, repoPath, branch, filePath string) error
	DeleteStaleChunks(ctx context.Context, repoPath, branch string, keepIDs []string) error

	GetIndexState(ctx context.Context, repoPath, branch string) (IndexState, bool, error)
	SetIndexState(ctx context.Context, state IndexState) error

	SearchSemantic(ctx context.Context, embedding []float32, repoPath, branch string, limit int) ([]SearchResult, error)
	SearchFullText(ctx context.Context, query, repoPath, branch string, limit int) ([]SearchResult, error)
	SearchHybrid(ctx context.Context, embedding []float32, query, repoPath, branch string, limit int) ([]SearchResult, error)

	Close() error
}
