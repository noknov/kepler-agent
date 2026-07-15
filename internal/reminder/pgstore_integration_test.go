package reminder

import (
	"context"
	"os"
	"testing"
	"time"
)

// Set REMINDER_TEST_POSTGRES_DSN to run this against a real PostgreSQL server.
func TestPGStoreIntegration(t *testing.T) {
	dsn := os.Getenv("REMINDER_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("REMINDER_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	store, err := NewPGStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := "test-reminder-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() { _, _ = store.pool.Exec(ctx, "DELETE FROM reminders WHERE id=$1", id) })
	if _, err := store.Create(ctx, Reminder{ID: id, UserID: "U-test", Channel: "C-test", Message: "test", RunAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	due, err := store.Due(ctx, time.Now())
	if err != nil || len(due) != 1 || due[0].ID != id {
		t.Fatalf("due = %#v, %v", due, err)
	}
	if err := store.MarkSent(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.List(ctx, "U-test"); err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
}
