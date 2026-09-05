package transcript

import (
	"context"
	"fmt"
	"sync"
)

type MemoryStore struct {
	mu     sync.Mutex
	events map[string][]Event
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{events: make(map[string][]Event)} }

func (s *MemoryStore) Append(_ context.Context, event Event) (Event, error) {
	if event.SessionID == "" {
		return Event{}, fmt.Errorf("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ID != "" {
		for _, existing := range s.events[event.SessionID] {
			if existing.ID == event.ID {
				return existing, nil
			}
		}
	}
	event.Sequence = uint64(len(s.events[event.SessionID]) + 1)
	s.events[event.SessionID] = append(s.events[event.SessionID], event)
	return event, nil
}

func (s *MemoryStore) AppendBatch(_ context.Context, events []Event) ([]Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	sessionID := events[0].SessionID
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	for _, event := range events {
		if event.SessionID != sessionID {
			return nil, fmt.Errorf("batch events must share one session id")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Event, 0, len(events))
	for _, event := range events {
		event.Sequence = uint64(len(s.events[sessionID]) + 1)
		s.events[sessionID] = append(s.events[sessionID], event)
		result = append(result, event)
	}
	return result, nil
}

func (s *MemoryStore) Load(_ context.Context, sessionID string, afterSequence uint64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.events[sessionID]
	if afterSequence >= uint64(len(events)) {
		return nil, nil
	}
	return append([]Event(nil), events[afterSequence:]...), nil
}
