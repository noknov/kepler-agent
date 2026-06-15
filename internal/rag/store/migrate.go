package store

import "fmt"

const tsvContentLimit = 200000

func migrationSQL(dims int) string {
	return fmt.Sprintf(`
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rag_chunks (
	id             TEXT PRIMARY KEY,
	repo_path      TEXT NOT NULL,
	file_path      TEXT NOT NULL,
	branch         TEXT NOT NULL,
	commit_sha     TEXT NOT NULL,
	start_line     INT NOT NULL,
	end_line       INT NOT NULL,
	chunk_type     TEXT NOT NULL,
	language       TEXT NOT NULL DEFAULT '',
	symbol_name    TEXT,
	parent_symbol  TEXT,
	package        TEXT,
	content        TEXT NOT NULL,
	context_prefix TEXT,
	content_hash   TEXT NOT NULL,
	embedding      vector(%[1]d),
	tsv            tsvector GENERATED ALWAYS AS (
		setweight(to_tsvector('english', coalesce(symbol_name, '')), 'A') ||
		setweight(to_tsvector('english', coalesce(package, '')), 'B') ||
		setweight(to_tsvector('english', left(content, %[2]d)), 'C')
	) STORED,
	created_at     TIMESTAMPTZ DEFAULT NOW(),
	updated_at     TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_repo_branch
	ON rag_chunks(repo_path, branch);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_repo_file
	ON rag_chunks(repo_path, branch, file_path);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_symbol
	ON rag_chunks(symbol_name) WHERE symbol_name IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_rag_chunks_content_hash
	ON rag_chunks(content_hash);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_tsv
	ON rag_chunks USING gin(tsv);

DO $$ BEGIN
	IF NOT EXISTS (
		SELECT 1 FROM pg_indexes WHERE indexname = 'idx_rag_chunks_embedding'
	) THEN
		CREATE INDEX idx_rag_chunks_embedding
			ON rag_chunks USING hnsw (embedding vector_cosine_ops)
			WITH (m = 16, ef_construction = 64);
	END IF;
END $$;

CREATE TABLE IF NOT EXISTS rag_index_state (
	repo_path   TEXT NOT NULL,
	branch      TEXT NOT NULL,
	last_commit TEXT NOT NULL,
	indexed_at  TIMESTAMPTZ DEFAULT NOW(),
	PRIMARY KEY (repo_path, branch)
);
`, dims, tsvContentLimit)
}
