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
	event.Sequence = uint64(len(s.events[event.SessionID]) + 1)
	s.events[event.SessionID] = append(s.events[event.SessionID], event)
	return event, nil
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
