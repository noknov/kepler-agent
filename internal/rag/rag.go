package rag

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/wati/oncall-agent/internal/rag/embedding"
	"github.com/wati/oncall-agent/internal/rag/indexer"
	"github.com/wati/oncall-agent/internal/rag/search"
	"github.com/wati/oncall-agent/internal/rag/store"
)

type Config struct {
	PostgresDSN      string
	EmbeddingBaseURL string
	EmbeddingAPIKey  string
	EmbeddingModel   string
	EmbeddingDims    int
	IndexInterval    time.Duration
	WorkspaceRoots   []string
}

type Manager struct {
	store    store.Store
	embedder *embedding.Client
	indexer  *indexer.Indexer
	engine   *search.Engine
	config   Config
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.EmbeddingDims <= 0 {
		cfg.EmbeddingDims = 1536
	}
	if cfg.IndexInterval <= 0 {
		cfg.IndexInterval = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pgStore, err := store.NewPGStore(ctx, cfg.PostgresDSN, cfg.EmbeddingDims)
	if err != nil {
		return nil, fmt.Errorf("rag: connect to postgres: %w", err)
	}

	if err := pgStore.Migrate(ctx); err != nil {
		pgStore.Close()
		return nil, fmt.Errorf("rag: migrate schema: %w", err)
	}

	emb := embedding.NewClient(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDims)
	idx := indexer.New(pgStore, emb)
	eng := &search.Engine{
		Store:    pgStore,
		Embedder: emb,
		Timeout:  10 * time.Second,
	}

	return &Manager{
		store:    pgStore,
		embedder: emb,
		indexer:  idx,
		engine:   eng,
		config:   cfg,
	}, nil
}

func (m *Manager) Search(ctx context.Context, query, repoPath, branch string, limit int) ([]search.Result, error) {
	return m.engine.Search(ctx, search.Query{
		Text:     query,
		RepoPath: repoPath,
		Branch:   branch,
		Limit:    limit,
	})
}

func (m *Manager) SearchSemantic(ctx context.Context, query, repoPath, branch string, limit int) ([]search.Result, error) {
	return m.engine.SearchSemanticOnly(ctx, search.Query{
		Text:     query,
		RepoPath: repoPath,
		Branch:   branch,
		Limit:    limit,
	})
}

func (m *Manager) IndexRepo(ctx context.Context, repoPath, branch string) (*indexer.IndexResult, error) {
	return m.indexer.IndexRepo(ctx, repoPath, branch)
}

// StartIndexLoop runs the background indexing loop. Blocks until ctx is cancelled.
func (m *Manager) StartIndexLoop(ctx context.Context) {
	loop := &indexer.Loop{
		Indexer:  m.indexer,
		Roots:    m.config.WorkspaceRoots,
		Interval: m.config.IndexInterval,
	}
	loop.Run(ctx)
}

func (m *Manager) Close() {
	if err := m.store.Close(); err != nil {
		log.Printf("rag: close store: %v", err)
	}
}
