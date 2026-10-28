package runtime

import (
	"context"
	"encoding/json"
	"sort"

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
	if len(s.events) > 0 && event.Sequence <= s.events[len(s.events)-1].Sequence {
		index := sort.Search(len(s.events), func(i int) bool { return s.events[i].Sequence >= event.Sequence })
		if s.events[index].Sequence == event.Sequence {
			return // Idempotent Store.Append returned an existing event.
		}
		// Concurrent durable appends may return out of sequence. Rebuild the
		// derived views in canonical order without mutating any published view.
		events := make([]transcript.Event, 0, len(s.events)+1)
		events = append(events, s.events[:index]...)
		events = append(events, event)
		events = append(events, s.events[index:]...)
		rebuilt := sessionEvents{requests: make(map[string]map[string]bool)}
		for _, stored := range events {
			rebuilt.append(stored)
		}
		*s = rebuilt
		return
	}
	s.events = append(s.events, event)
	switch event.Type {
	case transcript.UserInput, transcript.SteeringInput, transcript.AssistantMessage, transcript.ToolCallCompleted, transcript.ToolCallFailed, transcript.ApprovalRequested:
		s.context = append(s.context, event)
	case transcript.TaskStateUpdated:
		for i := len(s.context) - 1; i >= 0; i-- {
			if s.context[i].Type == transcript.TaskStateUpdated {
				s.context = append(s.context[:i:i], s.context[i+1:]...)
				break
			}
		}
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

func loadTurn(ctx context.Context, store transcript.Store, sessionID, turnID string) ([]transcript.Event, error) {
	if loader, ok := store.(transcript.TurnLoader); ok {
		return loader.LoadTurn(ctx, sessionID, turnID)
	}
	return store.Load(ctx, sessionID, 0)
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
