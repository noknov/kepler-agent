package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type indexedStateStore struct {
	transcript.Store
	events []transcript.Event
	err    error
}

func (s indexedStateStore) LoadLatestTurn(context.Context, string) ([]transcript.Event, error) {
	return s.events, s.err
}

func TestLatestTurnEventsUsesIndexedProjectionAndPropagatesFailure(t *testing.T) {
	store := indexedStateStore{events: []transcript.Event{
		{Type: transcript.TurnStarted, TurnID: "turn", Metadata: json.RawMessage(`{"user_id":"owner"}`)},
		{Type: transcript.TurnCompleted, TurnID: "turn", Status: string(TerminationPendingInput)},
	}}
	runner := &Runtime{deps: Dependencies{Transcript: store}}
	events, err := runner.LatestTurnEvents(t.Context(), "session")
	if err != nil || len(events) != 2 || events[0].TurnID != "turn" || !events[1].Type.IsTurnTerminal() {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	store.err = errors.New("indexed read unavailable")
	runner.deps.Transcript = store
	if _, err := runner.LatestTurnEvents(t.Context(), "session"); err != store.err {
		t.Fatalf("indexed read failure was hidden: %v", err)
	}
}
