package runtime

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// SessionState is the latest durable, terminal turn context for a session.
// Surfaces use it to restore the authenticated owner and waiting state.
type SessionState struct {
	TurnID      string
	UserID      string
	Scope       map[string]string
	Termination TerminationReason
}

// LatestSessionState returns the latest terminal turn together with the scope
// recorded when that turn started.
func (r *Runtime) LatestSessionState(ctx context.Context, sessionID string) (SessionState, bool, error) {
	var events []transcript.Event
	var err error
	if loader, ok := r.deps.Transcript.(transcript.TerminalTurnLoader); ok {
		events, err = loader.LoadLatestTerminalTurn(ctx, sessionID)
	} else {
		events, err = r.deps.Transcript.Load(ctx, sessionID, 0)
	}
	if err != nil {
		return SessionState{}, false, err
	}
	state := SessionState{}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if !event.Type.IsTurnTerminal() {
			continue
		}
		state.TurnID = event.TurnID
		state.Termination = TerminationReason(event.Status)
		break
	}
	if state.TurnID == "" {
		return SessionState{}, false, nil
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != transcript.TurnStarted || event.TurnID != state.TurnID {
			continue
		}
		var metadata struct {
			UserID string            `json:"user_id"`
			Scope  map[string]string `json:"scope"`
		}
		if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
			return SessionState{}, false, nil
		}
		state.UserID = metadata.UserID
		state.Scope = metadata.Scope
		return state, true, nil
	}
	return SessionState{}, false, nil
}

// SessionEvents returns the canonical events for rebuilding a non-authoritative
// presentation after a dropped or failed live projection.
func (r *Runtime) SessionEvents(ctx context.Context, sessionID string) ([]transcript.Event, error) {
	return r.deps.Transcript.Load(ctx, sessionID, 0)
}

// WaitingForInput reports whether the latest completed turn paused for input
// from userID. Surfaces use this canonical transcript state to decide whether
// an unaddressed thread reply belongs to the agent.
func (r *Runtime) WaitingForInput(ctx context.Context, sessionID, userID string) (bool, error) {
	state, ok, err := r.LatestSessionState(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return ok && state.Termination == TerminationPendingInput && state.UserID != "" && state.UserID == userID, nil
}

func (r *Runtime) LatestTurnEvents(ctx context.Context, sessionID string) ([]transcript.Event, error) {
	if loader, ok := r.deps.Transcript.(transcript.LatestTurnLoader); ok {
		return loader.LoadLatestTurn(ctx, sessionID)
	}
	events, err := r.deps.Transcript.Load(ctx, sessionID, 0)
	if err != nil {
		return nil, err
	}
	var started *transcript.Event
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == transcript.TurnStarted {
			started = &events[i]
			break
		}
	}
	if started == nil {
		return nil, nil
	}
	result := []transcript.Event{*started}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].TurnID == started.TurnID && events[i].Sequence > started.Sequence && events[i].Type.IsTurnTerminal() {
			result = append(result, events[i])
			break
		}
	}
	return result, nil
}

// ReconcileTurn applies a terminal presentation projection while holding the
// same session ownership as execution. A delayed receipt for an older turn is
// retired without touching the presentation of its successor.
func (r *Runtime) ReconcileTurn(ctx context.Context, sessionID, turnID string, apply func(context.Context, TerminationReason) error) (bool, error) {
	// Skip active work before waiting for its ownership lease. Recheck under
	// ownership before any external projection to close the snapshot race.
	observed, err := r.LatestTurnEvents(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if len(observed) == 0 {
		return false, nil
	}
	if observed[0].TurnID != turnID {
		return true, nil
	}
	if len(observed) < 2 || !observed[len(observed)-1].Type.IsTurnTerminal() {
		return false, nil
	}
	ctx, release, err := r.acquireSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	defer release()
	events, err := r.LatestTurnEvents(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, nil
	}
	if events[0].TurnID != turnID {
		return true, nil
	}
	if len(events) < 2 || !events[len(events)-1].Type.IsTurnTerminal() {
		return false, nil
	}
	terminal := events[len(events)-1]
	return true, apply(ctx, TerminationReason(terminal.Status))
}
