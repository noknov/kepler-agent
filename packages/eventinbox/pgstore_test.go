package eventinbox

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationStopPrecedesPromptBacklog(t *testing.T) {
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
	store := NewPGStore(pool)
	prefix := fmt.Sprintf("stop-priority-%d", time.Now().UnixNano())
	var ids []string
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM slack_event_inbox WHERE event_id=ANY($1)`, ids) }()
	for i := range 5 {
		id := fmt.Sprintf("%s-prompt-%d", prefix, i)
		ids = append(ids, id)
		if _, err := store.Claim(ctx, id, map[string]string{"type": "app_mention"}); err != nil {
			t.Fatal(err)
		}
	}
	stopID := prefix + "-stop"
	ids = append(ids, stopID)
	if _, err := store.Claim(ctx, stopID, map[string]string{"type": "agent_session_stopped"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.Pending(ctx, 1)
	if err != nil || len(items) != 1 || items[0].ID != stopID {
		t.Fatalf("stop stuck behind prompt page: records=%v err=%v", items, err)
	}
}
