package runtime

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// sessionEvents is turn-scoped derived state. Events and their payloads are
// immutable after append; clipped slice views can safely outlive a later append.
type sessionEvents struct {
	events            []transcript.Event
	context           []transcript.Event
	requestBoundaries map[string]map[string]uint64
	contextUsage      *contextUsageAnchor
	sampledThrough    uint64
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
		rebuilt := sessionEvents{requestBoundaries: make(map[string]map[string]uint64)}
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
		s.contextUsage = nil
		s.context = append(s.context, event)
		s.context = transcript.TurnWindow(s.context, "")
	case transcript.ModelCompleted:
		var completed struct {
			ContextUsage *contextUsageAnchor `json:"context_usage"`
			RequestID    string              `json:"request_id"`
			FinishReason model.FinishReason  `json:"finish_reason"`
			InputThrough uint64              `json:"input_through"`
		}
		// Missing/invalid measurements explicitly invalidate older observations.
		_ = json.Unmarshal(event.Metadata, &completed)
		s.contextUsage = nil
		boundary, admitted := s.requestBoundaries[event.TurnID][completed.RequestID]
		if admitted && completed.RequestID != "" && (completed.FinishReason == model.FinishStop || completed.FinishReason == model.FinishToolCalls) {
			s.contextUsage = completed.ContextUsage
			through := completed.InputThrough
			if through == 0 {
				// Legacy completions require their own admission receipt. A summary
				// or assistant wording is never delivery evidence.
				if completed.RequestID != "" {
					through = s.requestBoundaries[event.TurnID][completed.RequestID]
				}
			}
			if through > s.sampledThrough && through <= boundary && through < event.Sequence {
				s.sampledThrough = through
			}
		}
	case transcript.ModelRequestStarted:
		if s.requestBoundaries[event.TurnID] == nil {
			s.requestBoundaries[event.TurnID] = make(map[string]uint64)
		}
		var state modelRequestState
		key := event.ID
		if json.Unmarshal(event.Metadata, &state) == nil && state.RequestID != "" {
			key = state.RequestID
		}
		boundary := uint64(0)
		if event.Sequence > 0 {
			boundary = event.Sequence - 1
		}
		s.requestBoundaries[event.TurnID][key] = boundary
	}
}

func (r *Runtime) inputBoundary(sessionID string) uint64 {
	r.eventMu.RLock()
	defer r.eventMu.RUnlock()
	state := r.eventCache[sessionID]
	if state == nil {
		return 0
	}
	var through uint64
	for _, event := range state.context {
		if event.Type == transcript.UserInput || event.Type == transcript.SteeringInput {
			through = max(through, event.Sequence)
		}
	}
	return through
}

func loadTurn(ctx context.Context, store transcript.Store, sessionID, turnID string) ([]transcript.Event, error) {
	if loader, ok := store.(transcript.TurnLoader); ok {
		return loader.LoadTurn(ctx, sessionID, turnID)
	}
	return store.Load(ctx, sessionID, 0)
}

func (r *Runtime) setEventCache(sessionID string, events []transcript.Event) {
	state := &sessionEvents{requestBoundaries: make(map[string]map[string]uint64)}
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
