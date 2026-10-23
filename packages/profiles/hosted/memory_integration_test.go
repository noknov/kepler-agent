package hosted

import (
	"context"
	"encoding/json"
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

func TestIntegrationHybridMemoryCorpusAndExpiryQuotaCAS(t *testing.T) {
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
	team := "memory-lifecycle-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() {
		pool.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1`, team)
		pool.Exec(ctx, `DELETE FROM user_memory_generations WHERE team_id=$1`, team)
	}()
	var corpus struct {
		Version int
		Records []memory.Record
		Cases   []struct{ Query, Expected string }
	}
	data, err := os.ReadFile("../../memory/testdata/retrieval.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	store := memory.PGStore{Pool: pool}
	owner := memory.Identity{TeamID: team, UserID: "owner"}
	for _, record := range corpus.Records {
		if err := store.Put(ctx, owner, 0, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"lexical", "hybrid"} {
		hits := 0
		for _, test := range corpus.Cases {
			page, err := store.Query(ctx, owner, memory.Query{Text: test.Query, Mode: mode, Limit: 3, ExcludePreferences: true})
			if err != nil {
				t.Fatal(err)
			}
			if test.Expected == "" {
				if len(page.Records) != 0 {
					t.Fatalf("negative query matched: %+v", page.Records)
				}
				continue
			}
			found := false
			for _, record := range page.Records {
				if record.Key == test.Expected {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s missed %q in %+v", mode, test.Expected, page.Records)
			}
			hits++
		}
		t.Logf("fixture mode=%s recall@3=%d/4 (not a production benchmark)", mode, hits)
	}
	recalled, err := memory.Recall(ctx, store, owner, "PostgreSQL connection pool", memory.RecallPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundFact, foundPreference := false, false
	for _, record := range recalled.Records {
		foundFact = foundFact || record.Key == "db-pool"
		foundPreference = foundPreference || record.Kind == "preference"
	}
	if !foundFact || !foundPreference {
		t.Fatalf("hybrid recall/version fence lost records: %+v", recalled)
	}
	quotaOwner := memory.Identity{TeamID: team, UserID: "quota"}
	limited := memory.PGStore{Pool: pool, MaxRecords: 2}
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	first := memory.Record{Key: "first", Kind: "fact", Content: "temporary", ExpiresAt: &expiry}
	if err := limited.Put(ctx, quotaOwner, 0, first); err != nil {
		t.Fatal(err)
	}
	if err := limited.Put(ctx, quotaOwner, 0, memory.Record{Key: "second", Kind: "fact", Content: "stable"}); err != nil {
		t.Fatal(err)
	}
	if err := limited.Put(ctx, quotaOwner, 0, memory.Record{Key: "third", Kind: "fact", Content: "over quota"}); err == nil {
		t.Fatal("quota exceeded")
	}
	if err := limited.Put(ctx, quotaOwner, 0, memory.Record{Key: "first", Kind: "fact", Content: "blind overwrite"}); err == nil {
		t.Fatal("zero version overwrote existing memory")
	}
	beforeUpdate, err := limited.Query(ctx, quotaOwner, memory.Query{Keys: []string{"first"}})
	if err != nil || len(beforeUpdate.Records) != 1 {
		t.Fatalf("memory before update=%+v err=%v", beforeUpdate, err)
	}
	if err := limited.Put(ctx, quotaOwner, 0, memory.Record{Key: "first", Kind: "fact", Content: "updated", Version: beforeUpdate.Records[0].Version}); err != nil {
		t.Fatal(err)
	}
	page, err := limited.Query(ctx, quotaOwner, memory.Query{Text: "first", Mode: "hybrid"})
	if err != nil || len(page.Records) == 0 || page.Records[0].ExpiresAt == nil || !page.Records[0].ExpiresAt.Equal(expiry) {
		t.Fatalf("update removed expiry: %+v %v", page, err)
	}
	if err := limited.Put(ctx, quotaOwner, 0, memory.Record{Key: "first", Kind: "fact", Content: "permanent", Version: page.Records[0].Version, ClearExpiry: true}); err != nil {
		t.Fatal(err)
	}
	page, err = limited.Query(ctx, quotaOwner, memory.Query{Text: "first"})
	if err != nil || page.Records[0].ExpiresAt != nil {
		t.Fatalf("explicit clear failed: %+v %v", page, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_memories SET expires_at=NOW()-INTERVAL '1 hour' WHERE team_id=$1 AND user_id=$2 AND semantic_key='first'`, team, quotaOwner.UserID); err != nil {
		t.Fatal(err)
	}
	page, err = limited.Query(ctx, quotaOwner, memory.Query{Text: "first", Mode: "hybrid"})
	if err != nil || len(page.Records) != 0 {
		t.Fatalf("expired memory returned: %+v %v", page, err)
	}
	if err := limited.Put(ctx, quotaOwner, 0, memory.Record{Key: "third", Kind: "fact", Content: "reclaims explicit expiry"}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationMemoryRecreationRejectsOldVersion(t *testing.T) {
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
	identity := memory.Identity{TeamID: "memory-recreation-" + time.Now().UTC().Format("20060102150405.000000000"), UserID: "owner"}
	defer func() {
		pool.Exec(ctx, `DELETE FROM user_memories WHERE team_id=$1`, identity.TeamID)
		pool.Exec(ctx, `DELETE FROM user_memory_generations WHERE team_id=$1`, identity.TeamID)
	}()
	store := memory.PGStore{Pool: pool}
	put := func(content string, version int64) error {
		return store.Put(ctx, identity, 0, memory.Record{Key: "same", Kind: "fact", Content: content, Version: version})
	}
	if err := put("old fact", 0); err != nil {
		t.Fatal(err)
	}
	old, err := store.Query(ctx, identity, memory.Query{Keys: []string{"same"}})
	if err != nil || len(old.Records) != 1 {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	if err := store.Forget(ctx, identity, 0, "same"); err != nil {
		t.Fatal(err)
	}
	if err := put("replacement fact", 0); err != nil {
		t.Fatal(err)
	}
	if err := put("stale overwrite", old.Records[0].Version); err == nil {
		t.Fatal("old version overwrote a deleted and recreated memory")
	}
	current, err := store.Query(ctx, identity, memory.Query{Keys: []string{"same"}})
	if err != nil || len(current.Records) != 1 || current.Records[0].Content != "replacement fact" || current.Records[0].Version == old.Records[0].Version {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}
