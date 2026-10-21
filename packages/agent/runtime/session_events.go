package runtime

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// sessionEvents is turn-scoped derived state. Events and their payloads are
// immutable after append; clipped slice views can safely outlive a later append.
type sessionEvents struct {
	events   []transcript.Event
	context  []transcript.Event
	requests map[string]map[string]bool
}

func (s *sessionEvents) append(event transcript.Event) {
	// Idempotent Store.Append may return an existing event.
	if len(s.events) > 0 && event.Sequence <= s.events[len(s.events)-1].Sequence {
		return
	}
	s.events = append(s.events, event)
	switch event.Type {
	case transcript.UserInput, transcript.SteeringInput, transcript.AssistantMessage, transcript.ToolCallCompleted, transcript.ToolCallFailed, transcript.ApprovalRequested:
		s.context = append(s.context, event)
	case transcript.CompactionCreated:
		s.context = append(s.context, event)
		s.context = transcript.TurnWindow(s.context, "")
	case transcript.ModelRequestStarted:
		if s.requests[event.TurnID] == nil {
			s.requests[event.TurnID] = make(map[string]bool)
		}
		var state modelRequestState
		key := event.ID
		if json.Unmarshal(event.Metadata, &state) == nil && state.RequestID != "" {
			key = state.RequestID
		}
		s.requests[event.TurnID][key] = true
	}
}

func (r *Runtime) loadTurn(ctx context.Context, sessionID, turnID string) ([]transcript.Event, error) {
	if loader, ok := r.deps.Transcript.(transcript.TurnLoader); ok {
		return loader.LoadTurn(ctx, sessionID, turnID)
	}
	return r.deps.Transcript.Load(ctx, sessionID, 0)
}

func (r *Runtime) setEventCache(sessionID string, events []transcript.Event) {
	state := &sessionEvents{requests: make(map[string]map[string]bool)}
	for _, event := range events {
		state.append(event)
	}
	r.eventMu.Lock()
	r.eventCache[sessionID] = state
	r.eventMu.Unlock()
}

func (r *Runtime) clearEventCache(sessionID string) {
	r.eventMu.Lock()
	delete(r.eventCache, sessionID)
	r.eventMu.Unlock()
}

func (r *Runtime) turnEvents(ctx context.Context, sessionID string) ([]transcript.Event, error) {
	r.eventMu.RLock()
	if state := r.eventCache[sessionID]; state != nil {
		events := state.events[:len(state.events):len(state.events)]
		r.eventMu.RUnlock()
		return events, nil
	}
	r.eventMu.RUnlock()
	return r.deps.Transcript.Load(ctx, sessionID, 0)
}

func (r *Runtime) contextEvents(ctx context.Context, sessionID string) ([]transcript.Event, error) {
	r.eventMu.RLock()
	if state := r.eventCache[sessionID]; state != nil {
		events := state.context[:len(state.context):len(state.context)]
		r.eventMu.RUnlock()
		return events, nil
	}
	r.eventMu.RUnlock()
	return r.deps.Transcript.Load(ctx, sessionID, 0)
}
