package slackagent

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/connections"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func continuationFixture(t *testing.T) (*PGContinuationStore, connections.PGStore, ConnectionContinuation) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN must name an isolated database")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPGContinuationStore(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	c := ConnectionContinuation{UserID: "continuation-" + rand.Text(), Provider: connections.ProviderNotion, InstanceID: connections.DefaultInstanceID, TeamID: "T1", Channel: "C1", ThreadTS: "thread", TurnID: "waiting"}
	c.SessionID = (conversation.Target{Channel: c.Channel, ThreadTS: c.ThreadTS}).SessionID()
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM slack_connection_continuations WHERE user_id=$1`, `DELETE FROM user_connections WHERE user_id=$1`, `DELETE FROM connection_authorizations WHERE user_id=$1`} {
			if _, err := pool.Exec(context.Background(), q, c.UserID); err != nil {
				t.Error(err)
			}
		}
	})
	return store, connections.PGStore{Pool: pool, SecretKey: "fixture"}, c
}
func authorize(t *testing.T, s connections.PGStore, c ConnectionContinuation) {
	t.Helper()
	if err := s.UpsertTokenInstance(t.Context(), c.UserID, c.Provider, c.InstanceID, "", "token", nil, "", nil); err != nil {
		t.Fatal(err)
	}
}
func claimContinuations(t *testing.T, s *PGContinuationStore, want int) []ConnectionContinuation {
	t.Helper()
	out, err := s.ClaimReady(t.Context(), 16)
	if err != nil || len(out) != want {
		t.Fatalf("ready=%+v err=%v want=%d", out, err, want)
	}
	return out
}
func TestPGContinuationRecoversOfflineAndEarlyAuthorization(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{true: "early", false: "offline"}[early], func(t *testing.T) {
			s, auth, c := continuationFixture(t)
			if early {
				authorize(t, auth, c)
			}
			if err := s.Save(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if !early {
				claimContinuations(t, s, 0)
				authorize(t, auth, c)
			}
			// No subscriber or completion notification is involved. A new process reads facts.
			restarted := &PGContinuationStore{Pool: s.Pool}
			got := claimContinuations(t, restarted, 1)[0]
			if got.UserID != c.UserID || got.InstanceID != connections.DefaultInstanceID {
				t.Fatalf("identity=%+v", got)
			}
			claimContinuations(t, s, 0)
			if err := s.Clear(t.Context(), got); err != nil {
				t.Fatal(err)
			}
			claimContinuations(t, s, 0)
		})
	}
}
func TestRefreshDeletionAndOtherInstanceDoNotResumeWaitingTurn(t *testing.T) {
	s, auth, c := continuationFixture(t)
	authorize(t, auth, c)
	var err error
	c.AuthorizationRevision, err = auth.AuthorizationRevision(t.Context(), c.UserID, c.Provider, c.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	conn, _, err := auth.LoadCredential(t.Context(), c.UserID, c.Provider, c.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.UpdateCredential(t.Context(), conn, "refreshed token"); err != nil {
		t.Fatal(err)
	}
	claimContinuations(t, s, 0)
	other := c
	other.InstanceID = "other"
	authorize(t, auth, other)
	claimContinuations(t, s, 0)
	authorize(t, auth, c)
	if err := auth.DeleteInstance(t.Context(), c.UserID, c.Provider, c.InstanceID); err != nil {
		t.Fatal(err)
	}
	claimContinuations(t, s, 0)
	authorize(t, auth, c)
	claimContinuations(t, s, 1)
}
func TestContinuationCleanupCannotDeleteAnotherGenerationOrOwner(t *testing.T) {
	for _, replace := range []bool{false, true} {
		for _, clear := range []bool{false, true} {
			t.Run(map[bool]string{true: "replace", false: "expire"}[replace]+map[bool]string{true: "/clear", false: "/release"}[clear], func(t *testing.T) {
				s, auth, c := continuationFixture(t)
				if err := s.Save(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				authorize(t, auth, c)
				first := claimContinuations(t, s, 1)[0]
				if replace {
					c.TurnID = "new"
					if err := s.Save(t.Context(), c); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.Pool.Exec(t.Context(), `UPDATE slack_connection_continuations SET claim_until=NOW()-INTERVAL '1 second' WHERE user_id=$1`, c.UserID); err != nil {
						t.Fatal(err)
					}
				}
				second := claimContinuations(t, s, 1)[0]
				if clear {
					if err := s.Clear(t.Context(), first); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := s.Release(t.Context(), first); err != nil {
						t.Fatal(err)
					}
				}
				claimContinuations(t, s, 0)
				// Idempotent saves must not revoke another worker's current claim.
				if err := s.Save(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				claimContinuations(t, s, 0)
				if err := s.Release(t.Context(), second); err != nil {
					t.Fatal(err)
				}
				claimContinuations(t, s, 0) // Persisted retry deadline is respected.
				if _, err := s.Pool.Exec(t.Context(), `UPDATE slack_connection_continuations SET available_at=NOW() WHERE user_id=$1`, c.UserID); err != nil {
					t.Fatal(err)
				}
				recovered := claimContinuations(t, s, 1)[0]
				if recovered.TurnID != c.TurnID {
					t.Fatal("replacement turn lost")
				}
				if err := s.Clear(t.Context(), recovered); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestUnnamedConnectionContinuationHasOneStorageIdentity(t *testing.T) {
	s, auth, c := continuationFixture(t)
	c.InstanceID = ""
	if err := s.Save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	authorize(t, auth, c)
	got := claimContinuations(t, s, 1)[0]
	if got.InstanceID != connections.DefaultInstanceID {
		t.Fatalf("identity=%q", got.InstanceID)
	}
	if err := s.Clear(t.Context(), got); err != nil {
		t.Fatal(err)
	}
}

type failingContinuationInputs struct {
	memoryInputs
	fail bool
}

func (s *failingContinuationInputs) Enqueue(ctx context.Context, item sessioninput.Item) error {
	if s.fail {
		return fmt.Errorf("admission temporarily unavailable")
	}
	return s.memoryInputs.Enqueue(ctx, item)
}
func TestConnectionAdmissionRetriesAfterRestartWithoutAnotherNotification(t *testing.T) {
	store, auth, c := continuationFixture(t)
	if err := store.Save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	authorize(t, auth, c)
	service, _ := waitingConnectionService(t)
	inputs := &failingContinuationInputs{fail: true}
	service.Inputs = inputs
	service.Continuations = store
	service.recoverConnections(t.Context())
	if len(inputs.items) != 0 {
		t.Fatal("failed admission accepted input")
	}
	claimContinuations(t, store, 0)
	if _, err := store.Pool.Exec(t.Context(), `UPDATE slack_connection_continuations SET available_at=NOW() WHERE user_id=$1`, c.UserID); err != nil {
		t.Fatal(err)
	}
	restarted, _ := waitingConnectionService(t)
	inputs.fail = false
	restarted.Inputs = inputs
	restarted.Continuations = &PGContinuationStore{Pool: store.Pool}
	restarted.recoverConnections(t.Context())
	restarted.recoverConnections(t.Context())
	if len(inputs.items) != 1 {
		t.Fatalf("durable authorization admitted %d inputs", len(inputs.items))
	}
	claimContinuations(t, store, 0)
}
