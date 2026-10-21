package hosted

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/memory"
)

func TestIntegrationMemoryResetIsIsolatedAndFencesOldWriters(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	team := "memory-test-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_memory_reset_requests WHERE team_id IN ($1,$2)`, team, team+"-other")
		_, _ = pool.Exec(ctx, `DELETE FROM user_memories WHERE team_id IN ($1,$2)`, team, team+"-other")
		_, _ = pool.Exec(ctx, `DELETE FROM user_memory_generations WHERE team_id IN ($1,$2)`, team, team+"-other")
	}()
	store := memory.PGStore{Pool: pool}
	owner := memory.Identity{TeamID: team, UserID: "U1"}
	otherUser := memory.Identity{TeamID: team, UserID: "U2"}
	otherTeam := memory.Identity{TeamID: team + "-other", UserID: "U1"}
	for _, identity := range []memory.Identity{owner, otherUser, otherTeam} {
		if err := store.Put(ctx, identity, 0, memory.Record{Key: "preference", Kind: "preference", Content: "remember me"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Reset(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, owner, 0, memory.Record{Key: "stale", Kind: "fact", Content: "must not resurrect"}); err == nil {
		t.Fatal("pre-reset writer recreated memory")
	}
	generation, records, err := store.Snapshot(ctx, owner)
	if err != nil || generation != 1 || len(records) != 0 {
		t.Fatalf("generation=%d records=%v err=%v", generation, records, err)
	}
	for _, identity := range []memory.Identity{otherUser, otherTeam} {
		_, records, err := store.Snapshot(ctx, identity)
		if err != nil || len(records) != 1 {
			t.Fatalf("reset affected another identity: %+v %v", identity, err)
		}
	}
	if err := store.Put(ctx, owner, generation, memory.Record{Key: "new", Kind: "fact", Content: "newly remembered"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, owner, generation, memory.Record{Key: "new", Kind: "fact", Content: "stale overwrite", Version: 99}); err == nil {
		t.Fatal("stale memory version was accepted")
	}
	if err := store.ResetOnce(ctx, owner, "signed-interaction"); err != nil {
		t.Fatal(err)
	}
	generation, _, err = store.Snapshot(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, owner, generation, memory.Record{Key: "after-reset", Kind: "fact", Content: "new data"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetOnce(ctx, owner, "signed-interaction"); err != nil {
		t.Fatal(err)
	}
	_, records, err = store.Snapshot(ctx, owner)
	if err != nil || len(records) != 1 {
		t.Fatal("duplicate reset deleted newly saved memory", err)
	}
}
