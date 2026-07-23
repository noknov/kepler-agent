package rag

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/rag/embedding"
	"github.com/noknov/slack-copilot-agent/internal/rag/indexer"
	"github.com/noknov/slack-copilot-agent/internal/rag/search"
	"github.com/noknov/slack-copilot-agent/internal/rag/store"
)

type Config struct {
	PostgresDSN      string
	EmbeddingBaseURL string
	EmbeddingAPIKey  string
	EmbeddingModel   string
	EmbeddingDims    int
	BatchDelay       time.Duration
	IndexInterval    time.Duration
	WorkspaceRoots   []string
	Observer         indexer.Observer
}

type Manager struct {
	store    store.Store
	embedder *embedding.Client
	indexer  *indexer.Indexer
	engine   *search.Engine
	config   Config
	mu       sync.Mutex
	inFlight map[string]bool
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
	emb.BatchDelay = cfg.BatchDelay
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
		inFlight: map[string]bool{},
	}, nil
}

func (m *Manager) Search(ctx context.Context, query, repoPath, branch string, limit int) ([]search.Result, error) {
	if m == nil || m.engine == nil {
		return nil, fmt.Errorf("rag manager is not initialized")
	}
	return m.engine.Search(ctx, search.Query{
		Text:     query,
		RepoPath: repoPath,
		Branch:   branch,
		Limit:    limit,
	})
}

func (m *Manager) SearchSemantic(ctx context.Context, query, repoPath, branch string, limit int) ([]search.Result, error) {
	if m == nil || m.engine == nil {
		return nil, fmt.Errorf("rag manager is not initialized")
	}
	return m.engine.SearchSemanticOnly(ctx, search.Query{
		Text:     query,
		RepoPath: repoPath,
		Branch:   branch,
		Limit:    limit,
	})
}

func (m *Manager) IndexRepo(ctx context.Context, repoPath, branch string) (*indexer.IndexResult, error) {
	if m == nil || m.indexer == nil {
		return nil, fmt.Errorf("rag manager is not initialized")
	}
	return m.indexer.IndexRepo(ctx, repoPath, branch)
}

func (m *Manager) QueueIndex(repoPath, branch string) bool {
	if m == nil || m.indexer == nil {
		return false
	}
	key := repoPath + "@" + branch
	m.mu.Lock()
	if m.inFlight == nil {
		m.inFlight = map[string]bool{}
	}
	if m.inFlight[key] {
		m.mu.Unlock()
		return false
	}
	m.inFlight[key] = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.inFlight, key)
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		start := time.Now()
		result, err := m.indexer.IndexRepo(ctx, repoPath, branch)
		if err != nil {
			if m.config.Observer != nil {
				m.config.Observer.RAGIndexError(repoPath, branch, time.Since(start), err)
			}
			log.Printf("rag/indexer: on-demand index failed %s@%s: %v", repoPath, branch, err)
			return
		}
		if m.config.Observer != nil {
			m.config.Observer.RAGIndexSuccess(repoPath, branch, result.CommitSHA, result.FilesChanged, result.ChunksAdded, result.ChunksReused, result.ChunksSplitLarge, result.ChunksSkippedLarge, result.Duration)
		}
		log.Printf("rag/indexer: on-demand indexed %s@%s: %d files, %d chunks, %d reused, %d split_large, %d skipped_large in %s",
			repoPath, branch, result.FilesChanged, result.ChunksAdded, result.ChunksReused, result.ChunksSplitLarge, result.ChunksSkippedLarge, result.Duration)
	}()
	return true
}

func (m *Manager) IndexInFlight(repoPath, branch string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inFlight[repoPath+"@"+branch]
}

func (m *Manager) GetIndexState(ctx context.Context, repoPath, branch string) (store.IndexState, bool, error) {
	if m == nil || m.store == nil {
		return store.IndexState{}, false, fmt.Errorf("rag manager is not initialized")
	}
	return m.store.GetIndexState(ctx, repoPath, branch)
}

// StartIndexLoop runs the background indexing loop. Blocks until ctx is cancelled.
func (m *Manager) StartIndexLoop(ctx context.Context) {
	if m == nil || m.indexer == nil {
		log.Printf("rag/indexer: manager is not initialized, index loop not started")
		return
	}
	loop := &indexer.Loop{
		Indexer:  m.indexer,
		Roots:    m.config.WorkspaceRoots,
		Interval: m.config.IndexInterval,
		Observer: m.config.Observer,
	}
	loop.Run(ctx)
}

func (m *Manager) Close() {
	if m == nil || m.store == nil {
		return
	}
	if err := m.store.Close(); err != nil {
		log.Printf("rag: close store: %v", err)
	}
}
