package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type indexedStateStore struct {
	transcript.Store
	events []transcript.Event
	err    error
}

func (s indexedStateStore) LoadLatestTerminalTurn(context.Context, string) ([]transcript.Event, error) {
	return s.events, s.err
}

func TestLatestSessionStateUsesIndexedProjectionAndPropagatesFailure(t *testing.T) {
	store := indexedStateStore{events: []transcript.Event{
		{Type: transcript.TurnStarted, TurnID: "turn", Metadata: json.RawMessage(`{"user_id":"owner"}`)},
		{Type: transcript.TurnCompleted, TurnID: "turn", Status: string(TerminationPendingInput)},
	}}
	runner := &Runtime{deps: Dependencies{Transcript: store}}
	state, ok, err := runner.LatestSessionState(t.Context(), "session")
	if err != nil || !ok || state.UserID != "owner" || state.TurnID != "turn" {
		t.Fatalf("state=%+v ok=%v err=%v", state, ok, err)
	}
	store.err = errors.New("indexed read unavailable")
	runner.deps.Transcript = store
	if _, _, err := runner.LatestSessionState(t.Context(), "session"); err != store.err {
		t.Fatalf("indexed read failure was hidden: %v", err)
	}
}

func TestWaitingForInputUsesLatestTerminalTurnAndOwner(t *testing.T) {
	store := transcript.NewMemoryStore()
	metadata, _ := json.Marshal(map[string]any{"user_id": "U1", "scope": map[string]string{"workflow": "code_review"}})
	for _, event := range []transcript.Event{
		{ID: "start", SessionID: "s", TurnID: "t", Type: transcript.TurnStarted, Metadata: metadata},
		{ID: "done", SessionID: "s", TurnID: "t", Type: transcript.TurnCompleted, Status: string(TerminationPendingInput)},
	} {
		if _, err := store.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{responses: []model.Response{}}, Tools: catalog, Transcript: store})
	state, ok, err := runner.LatestSessionState(context.Background(), "s")
	if err != nil || !ok || state.UserID != "U1" || state.Scope["workflow"] != "code_review" || state.Termination != TerminationPendingInput {
		t.Fatalf("state=%+v ok=%v err=%v", state, ok, err)
	}
	if waiting, err := runner.WaitingForInput(context.Background(), "s", "U1"); err != nil || !waiting {
		t.Fatalf("waiting=%v err=%v", waiting, err)
	}
	if waiting, err := runner.WaitingForInput(context.Background(), "s", "U2"); err != nil || waiting {
		t.Fatalf("other user waiting=%v err=%v", waiting, err)
	}
	if _, err := store.Append(context.Background(), transcript.Event{ID: "new", SessionID: "s", TurnID: "new", Type: transcript.TurnCanceled}); err != nil {
		t.Fatal(err)
	}
	if waiting, err := runner.WaitingForInput(context.Background(), "s", "U1"); err != nil || waiting {
		t.Fatalf("stale pending state survived newer terminal turn: waiting=%v err=%v", waiting, err)
	}
}
