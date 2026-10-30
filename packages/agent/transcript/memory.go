package transcript

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type MemoryStore struct {
	mu     sync.Mutex
	events map[string][]memoryEvent
}

// Encoded facts are owned by the store. Every read decodes a caller-owned
// snapshot, matching the isolation of the persistent transcript adapters.
type memoryEvent struct {
	id   string
	data json.RawMessage
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{events: make(map[string][]memoryEvent)} }

func (s *MemoryStore) ReadEvent(_ context.Context, sessionID string, sequence uint64) (Event, error) {
	s.mu.Lock()
	events := s.events[sessionID]
	if sequence == 0 || sequence > uint64(len(events)) {
		s.mu.Unlock()
		return Event{}, fmt.Errorf("transcript event not found")
	}
	data := events[sequence-1].data
	s.mu.Unlock()
	return DecodeEvent(data)
}

func (s *MemoryStore) Append(_ context.Context, event Event) (Event, error) {
	if event.SessionID == "" {
		return Event{}, fmt.Errorf("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ID != "" {
		for _, existing := range s.events[event.SessionID] {
			if existing.id == event.ID {
				stored, err := DecodeEvent(existing.data)
				if err != nil {
					return Event{}, err
				}
				if stored.SessionID != event.SessionID || stored.TurnID != event.TurnID {
					return Event{}, fmt.Errorf("transcript event ID belongs to another owner")
				}
				return stored, nil
			}
		}
	}
	event.Sequence = uint64(len(s.events[event.SessionID]) + 1)
	data, err := json.Marshal(event)
	if err != nil {
		return Event{}, err
	}
	result, err := DecodeEvent(data)
	if err != nil {
		return Event{}, err
	}
	s.events[event.SessionID] = append(s.events[event.SessionID], memoryEvent{id: event.ID, data: data})
	return result, nil
}

func (s *MemoryStore) AppendBatch(ctx context.Context, events []Event) ([]Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	return s.appendBatch(ctx, events[0].SessionID, events, false)
}

func (s *MemoryStore) CreateSession(ctx context.Context, sessionID string, events []Event) ([]Event, error) {
	return s.appendBatch(ctx, sessionID, events, true)
}

func (s *MemoryStore) appendBatch(ctx context.Context, sessionID string, events []Event, create bool) ([]Event, error) {
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
	if create && len(s.events[sessionID]) != 0 {
		return nil, ErrSessionExists
	}
	result := make([]Event, 0, len(events))
	batch := make([]memoryEvent, 0, len(events))
	for index, event := range events {
		event.Sequence = uint64(len(s.events[sessionID]) + index + 1)
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		snapshot, err := DecodeEvent(data)
		if err != nil {
			return nil, err
		}
		batch = append(batch, memoryEvent{id: event.ID, data: data})
		result = append(result, snapshot)
	}
	s.events[sessionID] = append(s.events[sessionID], batch...)
	return result, nil
}

func (s *MemoryStore) Load(_ context.Context, sessionID string, afterSequence uint64) ([]Event, error) {
	s.mu.Lock()
	events := s.events[sessionID]
	if afterSequence >= uint64(len(events)) {
		s.mu.Unlock()
		return nil, nil
	}
	snapshot := append([]memoryEvent(nil), events[afterSequence:]...)
	s.mu.Unlock()
	result := make([]Event, 0, len(snapshot))
	for _, entry := range snapshot {
		event, err := DecodeEvent(entry.data)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, nil
}

func (s *MemoryStore) LoadTurn(ctx context.Context, sessionID, turnID string) ([]Event, error) {
	events, err := s.Load(ctx, sessionID, 0)
	if err != nil {
		return nil, err
	}
	return TurnWindow(events, turnID), nil
}
