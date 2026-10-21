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
	snapshot := func(ctx context.Context, id memory.Identity) (int64, []memory.Record, error) {
		page, err := store.Query(ctx, id, memory.Query{})
		return page.Generation, page.Records, err
	}
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
	generation, records, err := snapshot(ctx, owner)
	if err != nil || generation != 1 || len(records) != 0 {
		t.Fatalf("generation=%d records=%v err=%v", generation, records, err)
	}
	for _, identity := range []memory.Identity{otherUser, otherTeam} {
		_, records, err := snapshot(ctx, identity)
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
	generation, _, err = snapshot(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, owner, generation, memory.Record{Key: "after-reset", Kind: "fact", Content: "new data"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetOnce(ctx, owner, "signed-interaction"); err != nil {
		t.Fatal(err)
	}
	_, records, err = snapshot(ctx, owner)
	if err != nil || len(records) != 1 {
		t.Fatal("duplicate reset deleted newly saved memory", err)
	}
}

func TestIntegrationMemorySearchFindsOldMultilingualRecordsAndPaginates(t *testing.T) {
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
	team := "memory-search-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1`, team)
		_, _ = pool.Exec(ctx, `DELETE FROM user_memory_generations WHERE team_id=$1`, team)
	}()
	store := memory.PGStore{Pool: pool}
	owner := memory.Identity{TeamID: team, UserID: "U1"}
	other := memory.Identity{TeamID: team, UserID: "U2"}
	for _, record := range []memory.Record{{Key: "deployment", Kind: "project", Content: "Kepler 使用 SOPS 管理部署凭据"}, {Key: "language", Kind: "preference", Content: "Prefer concise replies"}} {
		if err := store.Put(ctx, owner, 0, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put(ctx, other, 0, memory.Record{Key: "private", Kind: "fact", Content: "部署凭据是怎么管理的"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_memories SET updated_at=NOW()-INTERVAL '1 year' WHERE team_id=$1 AND user_id=$2 AND semantic_key='deployment'`, team, owner.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_memories(team_id,user_id,semantic_key,kind,content) SELECT $1,$2,'noise-'||n,'fact','unrelated accounting event '||n FROM generate_series(1,230) n`, team, owner.UserID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"部署凭据是怎么管理的？", "deployment"} {
		page, err := store.Query(ctx, owner, memory.Query{Text: query, Limit: 8})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) == 0 || page.Records[0].Key != "deployment" {
			t.Fatalf("query=%q keys=%v", query, page.Records)
		}
	}
	page, err := store.Query(ctx, owner, memory.Query{Text: "zzzzzzzz", IncludePreferences: true, Limit: 8})
	if err != nil || len(page.Records) != 1 || page.Records[0].Key != "language" {
		t.Fatalf("preference selection=%v err=%v", page, err)
	}
	seen := map[string]bool{}
	offset := 0
	for {
		page, err := store.Query(ctx, owner, memory.Query{Limit: 100, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range page.Records {
			if seen[record.Key] {
				t.Fatal("duplicate page entry")
			}
			seen[record.Key] = true
			if record.Key == "private" {
				t.Fatal("cross-user leak")
			}
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if len(seen) != 232 {
		t.Fatalf("pagination lost records: %d", len(seen))
	}
}
