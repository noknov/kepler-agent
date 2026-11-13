package taskspace

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestPGSpaceGrantsAreImmutableAndWorkspaceBound(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN must name an isolated fixture")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := PGStore{Pool: pool}
	space := Space{TeamID: "taskspace-integration", Channel: t.Name(), OwnerID: "owner", OriginChannel: "origin", OriginMessageTS: "1"}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM slack_task_spaces WHERE team_id=$1`, space.TeamID)
	}()
	for range 2 {
		if err := store.Save(t.Context(), space); err != nil {
			t.Fatal(err)
		}
	}
	replacement := space
	replacement.OwnerID = "other"
	if err := store.Save(t.Context(), replacement); err == nil {
		t.Fatal("grant ownership overwritten")
	}
	if got, err := store.Get(t.Context(), space.TeamID, space.Channel); err != nil || got != space {
		t.Fatalf("stored grant: %+v %v", got, err)
	}
	if _, err := store.Get(t.Context(), "other-team", space.Channel); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace read: %v", err)
	}
}
