package hosted

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
)

func TestIntegrationPGTranscriptAppendAndReplay(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sessionID := "test-transcript-" + time.Now().UTC().Format("20060102150405.000000000")
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
	}()
	store := PGTranscript{Pool: pool}
	first, err := store.Append(context.Background(), transcript.Event{ID: sessionID + "-1", SessionID: sessionID, Type: transcript.SessionStarted, Timestamp: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(context.Background(), transcript.Event{ID: sessionID + "-2", SessionID: sessionID, TurnID: "turn", Type: transcript.TurnStarted, Timestamp: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(context.Background(), sessionID, first.Sequence)
	if err != nil || len(events) != 1 || events[0].Sequence != second.Sequence {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
