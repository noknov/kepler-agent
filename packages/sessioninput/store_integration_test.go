package sessioninput

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationFailedInputRetainsRetryDeadlineAndDeadLetter(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPGStore(pool)
	id := fmt.Sprintf("input-retry-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM agent_session_inputs WHERE id=$1`, id)
	if err := store.Enqueue(t.Context(), Item{ID: id, SessionID: id, Kind: KindQueue, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(t.Context(), id, KindQueue, "first", time.Minute, 1)
	if err != nil || len(first) != 1 || first[0].Attempts != 1 {
		t.Fatalf("claim=%+v error=%v", first, err)
	}
	if err := store.Renew(t.Context(), id, "other", time.Minute); !errors.Is(err, ErrClaimLost) {
		t.Fatal("wrong owner renewed input")
	}
	if dead, err := store.Fail(t.Context(), id, "first", errors.New("fixture"), time.Hour, 2); err != nil || dead {
		t.Fatalf("first failure dead=%v error=%v", dead, err)
	}
	if next, err := store.Claim(t.Context(), id, KindQueue, "second", time.Minute, 1); err != nil || len(next) != 0 {
		t.Fatalf("retry deadline ignored: %+v error=%v", next, err)
	}
	pool.Exec(t.Context(), `UPDATE agent_session_inputs SET available_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id)
	second, err := store.Claim(t.Context(), id, KindQueue, "second", time.Minute, 1)
	if err != nil || len(second) != 1 || second[0].Attempts != 2 {
		t.Fatalf("reclaim=%+v error=%v", second, err)
	}
	if err := store.Ack(t.Context(), id, "first"); !errors.Is(err, ErrClaimLost) {
		t.Fatal("old owner acknowledged a successor")
	}
	if _, err := store.Fail(t.Context(), id, "first", errors.New("old"), 0, 2); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("old owner failed successor: %v", err)
	}
	if dead, err := store.Fail(t.Context(), id, "second", errors.New("fixture"), 0, 2); err != nil || !dead {
		t.Fatalf("exhausted dead=%v error=%v", dead, err)
	}
	var acknowledged, dead bool
	if err := pool.QueryRow(t.Context(), `SELECT acknowledged_at IS NOT NULL,dead_lettered_at IS NOT NULL FROM agent_session_inputs WHERE id=$1`, id).Scan(&acknowledged, &dead); err != nil || acknowledged || !dead {
		t.Fatalf("failed input became success: acknowledged=%v dead=%v error=%v", acknowledged, dead, err)
	}
	if next, err := store.Claim(t.Context(), id, KindQueue, "third", time.Minute, 1); err != nil || len(next) != 0 {
		t.Fatalf("dead input was restarted: %+v error=%v", next, err)
	}
}

func TestIntegrationSteeringPromotionRequiresAnExpiredParentClaim(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPGStore(pool)
	id := fmt.Sprintf("steering-parent-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM agent_session_inputs WHERE session_id=$1`, id)
	for _, item := range []Item{{ID: id + "parent", SessionID: id, Kind: KindQueue, Payload: []byte(`{"actor":"owner"}`)}, {ID: id + "followup", SessionID: id, Kind: KindSteering, Payload: []byte(`{"actor":"owner"}`)}} {
		if err := store.Enqueue(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Claim(t.Context(), id, KindQueue, "worker", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	pool.Exec(t.Context(), `UPDATE agent_session_inputs SET created_at=NOW()-INTERVAL '1 hour' WHERE session_id=$1`, id)
	if _, err := store.PromoteExpiredSteering(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	var kind string
	pool.QueryRow(t.Context(), `SELECT kind FROM agent_session_inputs WHERE id=$1`, id+"followup").Scan(&kind)
	if kind != string(KindSteering) {
		t.Fatal("live parent lost its steering input")
	}
	pool.Exec(t.Context(), `UPDATE agent_session_inputs SET claim_until=NOW()-INTERVAL '1 second' WHERE id=$1`, id+"parent")
	if _, err := store.PromoteExpiredSteering(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	pool.QueryRow(t.Context(), `SELECT kind FROM agent_session_inputs WHERE id=$1`, id+"followup").Scan(&kind)
	if kind != string(KindQueue) {
		t.Fatal("orphaned steering was not recovered")
	}
}
