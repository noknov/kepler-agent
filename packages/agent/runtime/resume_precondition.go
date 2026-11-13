package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

var ErrStaleContinuation = errors.New("the waiting turn was superseded")

// Session ownership must already be held. A surface's earlier state lookup is
// a routing hint, not permission to resume after another turn has advanced.
func (r *Runtime) checkResumePrecondition(ctx context.Context, request TurnRequest) error {
	events, err := r.LatestTurnEvents(ctx, request.SessionID)
	if err != nil {
		return err
	}
	var started, terminal transcript.Event
	for _, event := range events {
		if event.Type == transcript.TurnStarted {
			started = event
			terminal = transcript.Event{}
		}
		if event.TurnID == started.TurnID && event.Type.IsTurnTerminal() {
			terminal = event
		}
	}
	if started.TurnID != request.ResumeFromTurnID || terminal.Type != transcript.TurnCompleted || terminal.Status != string(TerminationPendingInput) {
		return ErrStaleContinuation
	}
	var owner struct {
		UserID string            `json:"user_id"`
		Scope  map[string]string `json:"scope"`
	}
	if err := json.Unmarshal(started.Metadata, &owner); err != nil {
		return err
	}
	if owner.UserID == "" || owner.UserID != request.Scope.UserID || owner.Scope["team_id"] != request.Scope.Values["team_id"] {
		return ErrStaleContinuation
	}
	return nil
}
