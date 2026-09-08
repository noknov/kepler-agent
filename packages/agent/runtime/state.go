package runtime

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// SessionState is the latest durable, terminal turn context for a session.
// Surfaces use it to route follow-ups to an explicitly selected workflow
// without inferring workflow identity from conversational text.
type SessionState struct {
	TurnID      string
	UserID      string
	Scope       map[string]string
	Termination TerminationReason
}

// LatestSessionState returns the latest terminal turn together with the scope
// recorded when that turn started.
func (r *Runtime) LatestSessionState(ctx context.Context, sessionID string) (SessionState, bool, error) {
	events, err := r.deps.Transcript.Load(ctx, sessionID, 0)
	if err != nil {
		return SessionState{}, false, err
	}
	state := SessionState{}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != transcript.TurnCompleted && event.Type != transcript.TurnFailed && event.Type != transcript.TurnCanceled {
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
