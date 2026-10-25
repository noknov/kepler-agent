package memory

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Benchmark only the exact SQL retrieval path, excluding model/network latency.
func BenchmarkSemanticNamespace(b *testing.B) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		b.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	id := Identity{TeamID: "semantic-bench-" + rand.Text(), UserID: "owner"}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1`, id.TeamID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_memory_generations WHERE team_id=$1`, id.TeamID)
	}()
	_, err = pool.Exec(ctx, `INSERT INTO user_memory_generations(team_id,user_id,embedding_space) VALUES($1,$2,'benchmark')`, id.TeamID, id.UserID)
	if err != nil {
		b.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO user_memories(team_id,user_id,semantic_key,kind,content,embedding_space,embedding_version,embedding_vector)
 SELECT $1,$2,'fact-'||n,'fact','remembered project information','benchmark',0,ARRAY(SELECT 1.0/sqrt(1536) FROM generate_series(1,1536))::vector FROM generate_series(1,1000) n`, id.TeamID, id.UserID)
	if err != nil {
		b.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE user_memories SET embedding_version=version WHERE team_id=$1`, id.TeamID)
	if err != nil {
		b.Fatal(err)
	}
	vector := make([]float64, 1536)
	for i := range vector {
		vector[i] = 1
	}
	store := PGStore{Pool: pool}
	query := Query{Text: "环保出行", Mode: "semantic", EmbeddingSpace: "benchmark", Embedding: vector, Limit: 32}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, err := store.Query(ctx, id, query)
		if err != nil || len(page.Records) != 32 {
			b.Fatalf("page=%d err=%v", len(page.Records), err)
		}
	}
}
