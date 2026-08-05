package agentprotocol

import (
	"context"
	"sync"
)

const defaultHistoryLimit = 512

// Broker assigns per-thread sequence numbers and provides bounded replay for
// reconnecting transports. Slow subscribers drop events and reconnect using
// their last observed sequence instead of blocking the agent.
type Broker struct {
	mu           sync.Mutex
	historyLimit int
	sequence     map[string]uint64
	history      map[string][]Event
	subscribers  map[uint64]subscription
	nextSubID    uint64
}

type subscription struct {
	threadID string
	ch       chan Event
}

func NewBroker(historyLimit int) *Broker {
	if historyLimit <= 0 {
		historyLimit = defaultHistoryLimit
	}
	return &Broker{
		historyLimit: historyLimit,
		sequence:     map[string]uint64{},
		history:      map[string][]Event{},
		subscribers:  map[uint64]subscription{},
	}
}

func (b *Broker) Publish(_ context.Context, event Event) {
	if b == nil || event.Validate() != nil {
		return
	}
	event = Normalize(event)
	b.mu.Lock()
	b.sequence[event.ThreadID]++
	event.Sequence = b.sequence[event.ThreadID]
	history := append(b.history[event.ThreadID], event)
	if len(history) > b.historyLimit {
		history = append([]Event(nil), history[len(history)-b.historyLimit:]...)
	}
	b.history[event.ThreadID] = history
	for _, sub := range b.subscribers {
		if sub.threadID != "" && sub.threadID != event.ThreadID {
			continue
		}
		select {
		case sub.ch <- event:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *Broker) Subscribe(ctx context.Context, threadID string, after uint64) <-chan Event {
	ch := make(chan Event, b.historyLimit)
	if b == nil {
		close(ch)
		return ch
	}
	b.mu.Lock()
	for _, event := range b.history[threadID] {
		if event.Sequence > after {
			ch <- event
		}
	}
	b.nextSubID++
	id := b.nextSubID
	b.subscribers[id] = subscription{threadID: threadID, ch: ch}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if _, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(ch)
		}
		b.mu.Unlock()
	}()
	return ch
}
