package store

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/wati/oncall-agent/internal/rag/chunk"
)

type PGStore struct {
	pool *pgxpool.Pool
	dims int
}

func NewPGStore(ctx context.Context, dsn string, dims int) (*PGStore, error) {
	if dims <= 0 {
		dims = 1536
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	cfg.MaxConns = int32(envInt("RAG_POSTGRES_MAX_CONNS", envInt("POSTGRES_MAX_CONNS", 10)))
	cfg.MinConns = int32(envInt("RAG_POSTGRES_MIN_CONNS", 2))
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PGStore{pool: pool, dims: dims}, nil
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func (s *PGStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PGStore) Migrate(ctx context.Context) error {
	sql := migrationSQL(s.dims)
	_, err := s.pool.Exec(ctx, sql)
	return err
}

func (s *PGStore) UpsertChunks(ctx context.Context, records []ChunkRecord) error {
	if len(records) == 0 {
		return nil
	}

	const batchSize = 100
	for i := 0; i < len(records); i += batchSize {
		end := i + batchSize
		if end > len(records) {
			end = len(records)
		}
		if err := s.upsertBatch(ctx, records[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *PGStore) GetChunksForFile(ctx context.Context, repoPath, branch, filePath string) ([]ChunkRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, repo_path, file_path, branch, commit_sha,
			start_line, end_line, chunk_type, language,
			COALESCE(symbol_name, ''), COALESCE(parent_symbol, ''), COALESCE(package, ''),
			content, COALESCE(context_prefix, ''), content_hash,
			embedding::text
		FROM rag_chunks
		WHERE repo_path=$1 AND branch=$2 AND file_path=$3
	`, repoPath, branch, filePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ChunkRecord
	for rows.Next() {
		var r ChunkRecord
		var chunkType, symbolName, parentSymbol, pkg, contextPrefix string
		var embText *string
		if err := rows.Scan(
			&r.ID, &r.RepoPath, &r.FilePath, &r.Branch, &r.CommitSHA,
			&r.StartLine, &r.EndLine, &chunkType, &r.Language,
			&symbolName, &parentSymbol, &pkg,
			&r.Content, &contextPrefix, &r.ContentHash,
			&embText,
		); err != nil {
			return nil, err
		}
		r.ChunkType = chunk.Type(chunkType)
		r.SymbolName = symbolName
		r.ParentSymbol = parentSymbol
		r.Package = pkg
		r.ContextPrefix = contextPrefix
		if embText != nil {
			var v pgvector.Vector
			if err := v.Parse(*embText); err != nil {
				return nil, fmt.Errorf("parse embedding for chunk %s: %w", r.ID, err)
			}
			r.Embedding = v.Slice()
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (s *PGStore) upsertBatch(ctx context.Context, records []ChunkRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, r := range records {
		var emb *pgvector.Vector
		if len(r.Embedding) > 0 {
			v := pgvector.NewVector(r.Embedding)
			emb = &v
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO rag_chunks (
				id, repo_path, file_path, branch, commit_sha,
				start_line, end_line, chunk_type, language,
				symbol_name, parent_symbol, package,
				content, context_prefix, content_hash, embedding,
				updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NOW())
			ON CONFLICT (id) DO UPDATE SET
				commit_sha     = EXCLUDED.commit_sha,
				start_line     = EXCLUDED.start_line,
				end_line       = EXCLUDED.end_line,
				content        = EXCLUDED.content,
				context_prefix = EXCLUDED.context_prefix,
				content_hash   = EXCLUDED.content_hash,
				embedding      = COALESCE(EXCLUDED.embedding, rag_chunks.embedding),
				updated_at     = NOW()
		`, r.ID, r.RepoPath, r.FilePath, r.Branch, r.CommitSHA,
			r.StartLine, r.EndLine, string(r.ChunkType), r.Language,
			nullStr(r.SymbolName), nullStr(r.ParentSymbol), nullStr(r.Package),
			r.Content, nullStr(r.ContextPrefix), r.ContentHash, emb,
		)
		if err != nil {
			return fmt.Errorf("upsert chunk %s: %w", r.ID, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *PGStore) DeleteChunksForFile(ctx context.Context, repoPath, branch, filePath string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM rag_chunks WHERE repo_path=$1 AND branch=$2 AND file_path=$3`,
		repoPath, branch, filePath,
	)
	return err
}

func (s *PGStore) DeleteStaleChunks(ctx context.Context, repoPath, branch string, keepIDs []string) error {
	if len(keepIDs) == 0 {
		_, err := s.pool.Exec(ctx,
			`DELETE FROM rag_chunks WHERE repo_path=$1 AND branch=$2`,
			repoPath, branch,
		)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM rag_chunks WHERE repo_path=$1 AND branch=$2 AND id != ALL($3)`,
		repoPath, branch, keepIDs,
	)
	return err
}

func (s *PGStore) GetIndexState(ctx context.Context, repoPath, branch string) (IndexState, bool, error) {
	var state IndexState
	err := s.pool.QueryRow(ctx,
		`SELECT repo_path, branch, last_commit FROM rag_index_state WHERE repo_path=$1 AND branch=$2`,
		repoPath, branch,
	).Scan(&state.RepoPath, &state.Branch, &state.LastCommit)
	if err == pgx.ErrNoRows {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	return state, true, nil
}

func (s *PGStore) SetIndexState(ctx context.Context, state IndexState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rag_index_state (repo_path, branch, last_commit, indexed_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (repo_path, branch) DO UPDATE SET
			last_commit = EXCLUDED.last_commit,
			indexed_at  = NOW()
	`, state.RepoPath, state.Branch, state.LastCommit)
	return err
}

func (s *PGStore) SearchSemantic(ctx context.Context, embedding []float32, repoPath, branch string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	vec := pgvector.NewVector(embedding)

	rows, err := s.pool.Query(ctx, `
		SELECT id, repo_path, file_path, branch, commit_sha,
			start_line, end_line, chunk_type, language,
			COALESCE(symbol_name, ''), COALESCE(parent_symbol, ''), COALESCE(package, ''),
			content, COALESCE(context_prefix, ''), content_hash,
			1 - (embedding <=> $1::vector) AS score
		FROM rag_chunks
		WHERE repo_path = $2 AND branch = $3 AND embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT $4
	`, vec, repoPath, branch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResults(rows, "semantic")
}

func (s *PGStore) SearchFullText(ctx context.Context, query, repoPath, branch string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	tsQuery := toTSQuery(query)
	if tsQuery == "" {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, repo_path, file_path, branch, commit_sha,
			start_line, end_line, chunk_type, language,
			COALESCE(symbol_name, ''), COALESCE(parent_symbol, ''), COALESCE(package, ''),
			content, COALESCE(context_prefix, ''), content_hash,
			ts_rank(tsv, to_tsquery('english', $1)) AS score
		FROM rag_chunks
		WHERE repo_path = $2 AND branch = $3 AND tsv @@ to_tsquery('english', $1)
		ORDER BY score DESC
		LIMIT $4
	`, tsQuery, repoPath, branch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResults(rows, "fulltext")
}

func (s *PGStore) SearchHybrid(ctx context.Context, embedding []float32, query, repoPath, branch string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	vec := pgvector.NewVector(embedding)
	tsQuery := toTSQuery(query)
	if tsQuery == "" {
		return s.SearchSemantic(ctx, embedding, repoPath, branch, limit)
	}

	rows, err := s.pool.Query(ctx, `
		WITH semantic AS (
			SELECT id, 1 - (embedding <=> $1::vector) AS score
			FROM rag_chunks
			WHERE repo_path = $3 AND branch = $4 AND embedding IS NOT NULL
			ORDER BY embedding <=> $1::vector
			LIMIT $5 * 2
		),
		fulltext AS (
			SELECT id, ts_rank(tsv, to_tsquery('english', $2)) AS score
			FROM rag_chunks
			WHERE repo_path = $3 AND branch = $4 AND tsv @@ to_tsquery('english', $2)
			ORDER BY score DESC
			LIMIT $5 * 2
		),
		combined AS (
			SELECT
				COALESCE(s.id, f.id) AS id,
				COALESCE(s.score, 0) * 0.7 + COALESCE(f.score, 0) * 0.3 AS score
			FROM semantic s
			FULL OUTER JOIN fulltext f ON s.id = f.id
			ORDER BY score DESC
			LIMIT $5
		)
		SELECT c.id, c.repo_path, c.file_path, c.branch, c.commit_sha,
			c.start_line, c.end_line, c.chunk_type, c.language,
			COALESCE(c.symbol_name, ''), COALESCE(c.parent_symbol, ''), COALESCE(c.package, ''),
			c.content, COALESCE(c.context_prefix, ''), c.content_hash,
			cb.score
		FROM combined cb
		JOIN rag_chunks c ON c.id = cb.id
		ORDER BY cb.score DESC
	`, vec, tsQuery, repoPath, branch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResults(rows, "combined")
}

func scanResults(rows pgx.Rows, source string) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var chunkType, symbolName, parentSymbol, pkg, contextPrefix string
		err := rows.Scan(
			&r.ID, &r.RepoPath, &r.FilePath, &r.Branch, &r.CommitSHA,
			&r.StartLine, &r.EndLine, &chunkType, &r.Language,
			&symbolName, &parentSymbol, &pkg,
			&r.Content, &contextPrefix, &r.ContentHash,
			&r.Score,
		)
		if err != nil {
			return nil, err
		}
		r.ChunkType = chunk.Type(chunkType)
		r.SymbolName = symbolName
		r.ParentSymbol = parentSymbol
		r.Package = pkg
		r.ContextPrefix = contextPrefix
		r.Source = source
		results = append(results, r)
	}
	return results, rows.Err()
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toTSQuery(query string) string {
	words := strings.Fields(query)
	for i, w := range words {
		w = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
				return r
			}
			return -1
		}, w)
		if w != "" {
			words[i] = w + ":*"
		}
	}
	filtered := make([]string, 0, len(words))
	for _, w := range words {
		if w != ":*" {
			filtered = append(filtered, w)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return strings.Join(filtered, " & ")
}
