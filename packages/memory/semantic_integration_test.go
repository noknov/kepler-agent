package memory

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/agent/model"
)

type embeddingFunc func(context.Context, model.EmbeddingRequest) (model.EmbeddingResponse, error)

func (f embeddingFunc) Embed(ctx context.Context, r model.EmbeddingRequest) (model.EmbeddingResponse, error) {
	return f(ctx, r)
}

func semanticFixture(t *testing.T) (context.Context, SemanticStore, Identity) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1 // Provider callbacks can use SQL only if claims release connections.
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{TeamID: "semantic-" + rand.Text(), UserID: "owner"}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1`, id.TeamID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_memory_generations WHERE team_id=$1`, id.TeamID)
		pool.Close()
	})
	s := SemanticStore{PGStore: PGStore{Pool: pool}, Model: "fixture", Space: "fixture-v1", Dimensions: 2, BatchSize: 16, Timeout: time.Second}
	s.Client = embeddingFunc(func(_ context.Context, r model.EmbeddingRequest) (model.EmbeddingResponse, error) {
		vectors := make([][]float64, len(r.Input))
		for i := range vectors {
			vectors[i] = []float64{1, 0}
		}
		return model.EmbeddingResponse{Vectors: vectors}, nil
	})
	return ctx, s, id
}
func putSemanticFact(t *testing.T, ctx context.Context, s SemanticStore, id Identity) {
	t.Helper()
	if err := s.Put(ctx, id, 0, Record{Key: "commute", Kind: "fact", Content: "rides a bicycle"}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationSemanticRetrievalAndScope(t *testing.T) {
	ctx, s, id := semanticFixture(t)
	putSemanticFact(t, ctx, s, id)
	other := Identity{TeamID: id.TeamID, UserID: "other"}
	putSemanticFact(t, ctx, s, other)
	q := Query{Text: "环保出行", Mode: "hybrid"}
	lexical, err := s.PGStore.Query(ctx, id, q)
	if err != nil || len(lexical.Records) != 0 {
		t.Fatalf("control: %+v %v", lexical, err)
	}
	page, err := s.Query(ctx, id, q)
	if err != nil || len(page.Records) != 1 || page.Records[0].Key != "commute" {
		t.Fatalf("semantic: %+v %v", page, err)
	}
	var otherCached bool
	if err = s.Pool.QueryRow(ctx, `SELECT embedding_vector IS NOT NULL FROM user_memories WHERE team_id=$1 AND user_id=$2`, other.TeamID, other.UserID).Scan(&otherCached); err != nil || otherCached {
		t.Fatalf("indexed another user: %v %v", otherCached, err)
	}
	s.Client = embeddingFunc(func(context.Context, model.EmbeddingRequest) (model.EmbeddingResponse, error) {
		t.Fatal("exact key fences must not call embeddings")
		return model.EmbeddingResponse{}, nil
	})
	if _, err = s.Query(ctx, id, Query{Keys: []string{"commute"}}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationSemanticCacheCannotResurrectChangedSources(t *testing.T) {
	for _, action := range []string{"update", "delete-recreate", "reset", "expire", "switch"} {
		t.Run(action, func(t *testing.T) {
			ctx, s, id := semanticFixture(t)
			putSemanticFact(t, ctx, s, id)
			generation, err := s.ensureSpace(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			s.Client = embeddingFunc(func(call context.Context, r model.EmbeddingRequest) (model.EmbeddingResponse, error) {
				// Mutate while provider is running: no pool connection is held.
				page, e := s.PGStore.Query(call, id, Query{})
				if e != nil {
					return model.EmbeddingResponse{}, e
				}
				switch action {
				case "update":
					record := page.Records[0]
					record.Content = "walks to work"
					err = s.Put(call, id, 0, record)
				case "delete-recreate":
					err = s.Forget(call, id, 0, "commute")
					if err == nil {
						err = s.Put(call, id, 0, Record{Key: "commute", Kind: "fact", Content: "walks to work"})
					}
				case "reset":
					err = s.Reset(call, id)
				case "expire":
					_, err = s.Pool.Exec(call, `UPDATE user_memories SET expires_at=NOW()-INTERVAL '1 second' WHERE team_id=$1`, id.TeamID)
				case "switch":
					next := s
					next.Space = "fixture-v2"
					err = next.SwitchSpace(call, id, s.Space)
				}
				return model.EmbeddingResponse{Vectors: [][]float64{{1, 0}}}, err
			})
			if err = s.index(ctx, id, generation); err != nil {
				t.Fatal(err)
			}
			var count int
			err = s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_memories WHERE team_id=$1 AND embedding_vector IS NOT NULL`, id.TeamID).Scan(&count)
			if err != nil || count != 0 {
				t.Fatalf("stale result persisted: %d %v", count, err)
			}
		})
	}
}

func TestIntegrationSemanticConcurrentClaimsAndSpaceCAS(t *testing.T) {
	ctx, s, id := semanticFixture(t)
	putSemanticFact(t, ctx, s, id)
	generation, err := s.ensureSpace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.Client = embeddingFunc(func(ctx context.Context, r model.EmbeddingRequest) (model.EmbeddingResponse, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return model.EmbeddingResponse{}, ctx.Err()
		}
		return model.EmbeddingResponse{Vectors: [][]float64{{1, 0}}}, nil
	})
	done := make(chan error, 1)
	go func() { done <- s.index(ctx, id, generation) }()
	<-entered
	err = s.index(ctx, id, generation)
	close(release)
	first := <-done
	if err != nil || first != nil || calls.Load() != 1 {
		t.Fatalf("duplicate claims: %d %v %v", calls.Load(), err, first)
	}
	next := s
	next.Space = "fixture-v2"
	if err = next.SwitchSpace(ctx, id, "wrong"); !errors.Is(err, ErrEmbeddingSpace) {
		t.Fatal(err)
	}
	if err = next.SwitchSpace(ctx, id, s.Space); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ensureSpace(ctx, id); !errors.Is(err, ErrEmbeddingSpace) {
		t.Fatal("old deployment changed active space", err)
	}
}

func TestIntegrationSemanticFailureReleasesClaimAndFallsBack(t *testing.T) {
	ctx, s, id := semanticFixture(t)
	putSemanticFact(t, ctx, s, id)
	s.Client = embeddingFunc(func(context.Context, model.EmbeddingRequest) (model.EmbeddingResponse, error) {
		return model.EmbeddingResponse{}, errors.New("unavailable")
	})
	degraded := false
	s.Degraded = func(error) { degraded = true }
	page, err := s.Query(ctx, id, Query{Text: "bicycle", Mode: "hybrid"})
	if err != nil || len(page.Records) != 1 || !degraded {
		t.Fatalf("fallback: %+v %v %v", page, err, degraded)
	}
	var count int
	err = s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_memories WHERE team_id=$1 AND embedding_claim_owner<>''`, id.TeamID).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("leaked claims: %d %v", count, err)
	}
}

func TestIntegrationHybridWorksWithoutEmbeddingColumns(t *testing.T) {
	ctx, s, id := semanticFixture(t)
	_, err := s.Pool.Exec(ctx, `CREATE TEMP TABLE user_memory_generations(team_id text,user_id text,generation bigint);
 CREATE TEMP TABLE user_memories(team_id text,user_id text,generation bigint,semantic_key text,kind text,content text,source_session_id text,source_turn_id text,version bigint,updated_at timestamptz,expires_at timestamptz)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO user_memories VALUES($1,$2,0,'commute','fact','rides a bicycle','','',1,NOW(),NULL)`, id.TeamID, id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.PGStore.Query(ctx, id, Query{Text: "bicycle", Mode: "hybrid"})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("disabled semantic capability requires new schema: %+v %v", page, err)
	}
}
