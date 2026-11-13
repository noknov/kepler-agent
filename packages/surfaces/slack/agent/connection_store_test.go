package slackagent

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
)

func continuationRedis(t *testing.T) *redisclient.Client {
	t.Helper()
	address := os.Getenv("REDIS_TEST_URL")
	if address == "" {
		t.Skip("REDIS_TEST_URL must point to an isolated Redis fixture")
	}
	client, err := redisclient.New(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRedisContinuationStoreRoundTrip(t *testing.T) {
	client := continuationRedis(t)

	store := NewRedisContinuationStore(client)
	ctx := context.Background()
	cont := ConnectionContinuation{
		UserID:     "U-test-" + rand.Text(),
		Provider:   connections.ProviderNotion,
		InstanceID: "i-prod",
		SessionID:  "C1:T1",
		TurnID:     "waiting-turn",
		Channel:    "C1",
		ThreadTS:   "T1",
	}
	if err := store.Save(ctx, cont); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Claim(ctx, cont.UserID, cont.Provider, cont.InstanceID)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Claim() = (%+v, %v)", loaded, err)
	}
	if loaded[0].Channel != cont.Channel || loaded[0].ThreadTS != cont.ThreadTS {
		t.Fatalf("Claim() = %+v, want %+v", loaded[0], cont)
	}
	if err := store.Release(ctx, loaded[0]); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	loaded, err = store.Claim(ctx, cont.UserID, cont.Provider, cont.InstanceID)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Claim() after release = (%+v, %v)", loaded, err)
	}
	if err := store.Clear(ctx, loaded[0]); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	loaded, err = store.Claim(ctx, cont.UserID, cont.Provider, cont.InstanceID)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("Claim() after clear = (%+v, %v)", loaded, err)
	}
}

func TestContinuationCleanupCannotDeleteAnotherGenerationOrOwner(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		for _, clear := range []bool{false, true} {
			t.Run(fmt.Sprintf("replacement=%t/clear=%t", replacement, clear), func(t *testing.T) {
				client := continuationRedis(t)
				store := NewRedisContinuationStore(client)
				pending := ConnectionContinuation{UserID: "U-owner-" + rand.Text(), Provider: connections.ProviderNotion, InstanceID: "i-prod", SessionID: "session", TurnID: "old", Channel: "C1", ThreadTS: "thread"}
				if err := store.Save(t.Context(), pending); err != nil {
					t.Fatal(err)
				}
				first, err := store.Claim(t.Context(), pending.UserID, pending.Provider, pending.InstanceID)
				if err != nil || len(first) != 1 {
					t.Fatalf("first claim=%+v err=%v", first, err)
				}
				if replacement {
					pending.TurnID = "new"
					if err := store.Save(t.Context(), pending); err != nil {
						t.Fatal(err)
					}
				} else {
					// Simulate expiry of the old lease without a clock-dependent sleep.
					if err := client.Del(t.Context(), continuationClaimKey(pending)); err != nil {
						t.Fatal(err)
					}
				}
				second, err := store.Claim(t.Context(), pending.UserID, pending.Provider, pending.InstanceID)
				if err != nil || len(second) != 1 {
					t.Fatalf("second claim=%+v err=%v", second, err)
				}
				if clear {
					err = store.Clear(t.Context(), first[0])
				} else {
					err = store.Release(t.Context(), first[0])
				}
				if err != nil {
					t.Fatal(err)
				}
				competing, err := store.Claim(t.Context(), pending.UserID, pending.Provider, pending.InstanceID)
				if err != nil || len(competing) != 0 {
					t.Fatalf("stale cleanup released current owner: %+v %v", competing, err)
				}
				if err := store.Release(t.Context(), second[0]); err != nil {
					t.Fatal(err)
				}
				recovered, err := store.Claim(t.Context(), pending.UserID, pending.Provider, pending.InstanceID)
				if err != nil || len(recovered) != 1 || recovered[0].TurnID != pending.TurnID {
					t.Fatalf("stale cleanup removed current payload: %+v %v", recovered, err)
				}
				if err := store.Clear(t.Context(), recovered[0]); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestUnnamedConnectionContinuationHasOneStorageIdentity(t *testing.T) {
	client := continuationRedis(t)
	store := NewRedisContinuationStore(client)
	pending := ConnectionContinuation{UserID: "U-unnamed-" + rand.Text(), Provider: connections.ProviderNotion, SessionID: "session", TurnID: "waiting", Channel: "C1"}
	if err := store.Save(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(t.Context(), pending.UserID, pending.Provider, connections.DefaultInstanceID)
	if err != nil || len(first) != 1 || first[0].InstanceID != connections.DefaultInstanceID {
		t.Fatalf("unnamed completion could not find its continuation: %+v %v", first, err)
	}
	if err := store.Release(t.Context(), first[0]); err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(t.Context(), pending.UserID, pending.Provider, "")
	if err != nil || len(second) != 1 {
		t.Fatalf("unnamed identity diverged: %+v %v", second, err)
	}
	if err := store.Clear(t.Context(), second[0]); err != nil {
		t.Fatal(err)
	}
}
