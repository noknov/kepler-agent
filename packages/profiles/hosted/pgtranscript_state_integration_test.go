package hosted

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func stateFixture(t testing.TB) (PGTranscript, string) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	session := "state-" + rand.Text()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1 OR session_id=$2`, session, session+"-other")
		pool.Close()
	})
	return PGTranscript{Pool: pool}, session
}

// Suppressing only the indexed capability exercises the original projection.
type fullStateStore struct{ transcript.Store }

func stateRuntime(t testing.TB, store transcript.Store) *agentruntime.Runtime {
	t.Helper()
	catalog, _ := tool.NewCatalog()
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: &identityModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestIntegrationLatestTerminalStateMatchesFullHistory(t *testing.T) {
	store, session := stateFixture(t)
	indexed, full := stateRuntime(t, store), stateRuntime(t, fullStateStore{store})
	for _, kind := range []transcript.EventType{transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled} {
		turn := string(kind)
		for _, event := range []transcript.Event{
			{Type: transcript.TurnStarted, TurnID: turn, Metadata: json.RawMessage(`{"user_id":"owner","scope":{"workflow":"review"}}`)},
			{Type: kind, TurnID: turn, Status: "pending_input"},
			{Type: transcript.TurnStarted, TurnID: turn + "-active", Metadata: json.RawMessage(`{"user_id":"new-owner"}`)},
		} {
			event.ID, event.SessionID, event.Timestamp = rand.Text(), session, time.Now()
			if _, err := store.Append(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
		a, okA, errA := indexed.LatestSessionState(t.Context(), session)
		b, okB, errB := full.LatestSessionState(t.Context(), session)
		if errA != nil || errB != nil || !okA || okA != okB || !reflect.DeepEqual(a, b) || a.TurnID != turn {
			t.Fatalf("indexed=%+v/%v/%v full=%+v/%v/%v", a, okA, errA, b, okB, errB)
		}
		facts, err := store.LoadLatestTerminalTurn(t.Context(), session)
		if err != nil || len(facts) != 2 {
			t.Fatalf("state read not bounded: %d %v", len(facts), err)
		}
	}
	for _, event := range []transcript.Event{
		{SessionID: session + "-other", TurnID: "foreign", Type: transcript.TurnCompleted},
		{SessionID: session, TurnID: "missing-start", Type: transcript.TurnCanceled},
	} {
		event.ID, event.Timestamp = rand.Text(), time.Now()
		if _, err := store.Append(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := indexed.LatestSessionState(t.Context(), session); err != nil || ok {
		t.Fatalf("missing start selected another turn: %v %v", ok, err)
	}
	if events, err := store.LoadLatestTerminalTurn(t.Context(), session+"-absent"); err != nil || len(events) != 0 {
		t.Fatalf("absent session: %+v %v", events, err)
	}
}

func BenchmarkLatestSessionState(b *testing.B) {
	store, session := stateFixture(b)
	events := []transcript.Event{{Type: transcript.TurnStarted, Metadata: json.RawMessage(`{"user_id":"owner"}`)}}
	message := model.TextMessage(model.RoleTool, strings.Repeat("evidence ", 4096))
	for range 256 {
		events = append(events, transcript.Event{Type: transcript.ToolCallCompleted, Message: &message})
	}
	events = append(events, transcript.Event{Type: transcript.TurnCompleted, Status: "completed"})
	for i := range events {
		events[i].ID, events[i].SessionID, events[i].TurnID, events[i].Timestamp = rand.Text(), session, "turn", time.Now()
	}
	if _, err := store.AppendBatch(context.Background(), events); err != nil {
		b.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		store transcript.Store
	}{{"full-history", fullStateStore{store}}, {"indexed", store}} {
		b.Run(test.name, func(b *testing.B) {
			runner := stateRuntime(b, test.store)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, ok, err := runner.LatestSessionState(context.Background(), session); err != nil || !ok {
					b.Fatalf("ok=%v err=%v", ok, err)
				}
			}
		})
	}
}
