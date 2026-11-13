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

type advancingResumeLease struct{ store transcript.Store }

func (l advancingResumeLease) Lock(ctx context.Context, id string) (func(), error) {
	_, err := l.store.Append(ctx, transcript.Event{SessionID: "s", TurnID: "newer", Type: transcript.TurnStarted, Metadata: json.RawMessage(`{"user_id":"owner","scope":{"team_id":"team"}}`)})
	return func() {}, err
}

func TestResumePreconditionIsCheckedUnderSessionOwnership(t *testing.T) {
	for _, advance := range []bool{false, true} {
		t.Run(map[bool]string{false: "still-waiting", true: "advanced-during-acquire"}[advance], func(t *testing.T) {
			store := transcript.NewMemoryStore()
			for _, event := range []transcript.Event{
				{SessionID: "s", TurnID: "waiting", Type: transcript.TurnStarted, Metadata: json.RawMessage(`{"user_id":"owner","scope":{"team_id":"team"}}`)},
				{SessionID: "s", TurnID: "waiting", Type: transcript.TurnCompleted, Status: string(TerminationPendingInput)},
			} {
				if _, err := store.Append(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			catalog, _ := tool.NewCatalog()
			client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}
			deps := Dependencies{Model: client, Tools: catalog, Transcript: store}
			if advance {
				deps.Lease = advancingResumeLease{store: store}
			}
			runner, err := New(Config{}, deps)
			if err != nil {
				t.Fatal(err)
			}
			request := TurnRequest{SessionID: "s", TurnID: "resume", ResumeFromTurnID: "waiting", Input: model.TextMessage(model.RoleUser, "continue"), Scope: tool.Scope{UserID: "owner", Values: map[string]string{"team_id": "team"}}}
			_, err = runner.RunTurn(t.Context(), request)
			if advance {
				if !errors.Is(err, ErrStaleContinuation) || len(client.requests) != 0 {
					t.Fatalf("stale resume executed: err=%v calls=%d", err, len(client.requests))
				}
				events, _ := store.Load(t.Context(), "s", 0)
				if hasTurnEvent(events, "resume", transcript.TurnStarted) {
					t.Fatal("stale resume wrote a new turn")
				}
				return
			}
			if err != nil || len(client.requests) != 1 {
				t.Fatalf("valid resume failed: err=%v calls=%d", err, len(client.requests))
			}
			if _, err := runner.RunTurn(t.Context(), request); err != nil || len(client.requests) != 1 {
				t.Fatalf("completed resume was not replayed: %v", err)
			}
		})
	}
}

func TestResumePreconditionBindsOwnerWorkspaceAndWaitingOutcome(t *testing.T) {
	for _, test := range []struct {
		name, user, team string
		terminal         transcript.EventType
		status           string
	}{
		{"another actor", "other", "team", transcript.TurnCompleted, string(TerminationPendingInput)},
		{"another workspace", "owner", "other-team", transcript.TurnCompleted, string(TerminationPendingInput)},
		{"completed", "owner", "team", transcript.TurnCompleted, string(TerminationCompleted)},
		{"failed", "owner", "team", transcript.TurnFailed, string(TerminationPendingInput)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := transcript.NewMemoryStore()
			for _, event := range []transcript.Event{
				{SessionID: "s", TurnID: "waiting", Type: transcript.TurnStarted, Metadata: json.RawMessage(`{"user_id":"owner","scope":{"team_id":"team"}}`)},
				{SessionID: "s", TurnID: "waiting", Type: test.terminal, Status: test.status},
			} {
				if _, err := store.Append(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			runner := &Runtime{deps: Dependencies{Transcript: store}}
			err := runner.checkResumePrecondition(t.Context(), TurnRequest{SessionID: "s", ResumeFromTurnID: "waiting", Scope: tool.Scope{UserID: test.user, Values: map[string]string{"team_id": test.team}}})
			if !errors.Is(err, ErrStaleContinuation) {
				t.Fatalf("unmatched waiting state admitted: %v", err)
			}
		})
	}
}
