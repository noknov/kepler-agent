package appserver

import (
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

const (
	defaultDeltaFlushInterval = 24 * time.Millisecond
	defaultDeltaFlushBytes    = 32
)

// deltaBatcher coalesces adjacent text deltas for one app-server client.
//
// The first delta of each stream is delivered immediately for a fast TTFT.
// Subsequent deltas flush at a short, bounded cadence or when enough text has
// accumulated. This keeps the model stream responsive while preventing tiny
// provider chunks from causing a JSON-RPC write and terminal render each.
type deltaBatcher struct {
	mu       sync.Mutex
	interval time.Duration
	maxBytes int
	emit     func(transcript.Event)
	pending  map[deltaStreamKey]*pendingDelta
}

type deltaStreamKey struct {
	turnID string
	itemID string
	phase  string
}

type pendingDelta struct {
	event     transcript.Event
	text      strings.Builder
	lastFlush time.Time
	emitted   bool
	timer     *time.Timer
}

func newDeltaBatcher(interval time.Duration, maxBytes int, emit func(transcript.Event)) *deltaBatcher {
	if interval <= 0 {
		interval = defaultDeltaFlushInterval
	}
	if maxBytes <= 0 {
		maxBytes = defaultDeltaFlushBytes
	}
	return &deltaBatcher{
		interval: interval,
		maxBytes: maxBytes,
		emit:     emit,
		pending:  make(map[deltaStreamKey]*pendingDelta),
	}
}

func (b *deltaBatcher) push(event transcript.Event) {
	if b == nil || b.emit == nil || event.Model == nil || event.Model.Text == "" {
		return
	}

	key := deltaStreamKey{turnID: event.TurnID, itemID: event.Model.ItemID, phase: event.Model.Phase}
	now := time.Now()
	b.mu.Lock()
	pending := b.pending[key]
	if pending == nil {
		pending = &pendingDelta{}
		b.pending[key] = pending
	}
	pending.event = event
	pending.text.WriteString(event.Model.Text)
	shouldFlush := !pending.emitted || pending.text.Len() >= b.maxBytes || now.Sub(pending.lastFlush) >= b.interval
	if shouldFlush {
		flushed, ok := b.takeLocked(pending, now)
		b.mu.Unlock()
		if ok {
			b.emit(flushed)
		}
		return
	}
	if pending.timer == nil {
		delay := b.interval - now.Sub(pending.lastFlush)
		pending.timer = time.AfterFunc(delay, func() { b.flush(key) })
	}
	b.mu.Unlock()
}

// flushTurn emits every buffered delta for a completed or interrupted turn.
// It is deliberately synchronous so turn/completed can never overtake text.
func (b *deltaBatcher) flushTurn(turnID string) {
	if b == nil {
		return
	}
	now := time.Now()
	var flushed []transcript.Event
	b.mu.Lock()
	for key, pending := range b.pending {
		if key.turnID != turnID {
			continue
		}
		if event, ok := b.takeLocked(pending, now); ok {
			flushed = append(flushed, event)
		}
		delete(b.pending, key)
	}
	b.mu.Unlock()
	for _, event := range flushed {
		b.emit(event)
	}
}

func (b *deltaBatcher) flush(key deltaStreamKey) {
	now := time.Now()
	b.mu.Lock()
	pending := b.pending[key]
	if pending == nil {
		b.mu.Unlock()
		return
	}
	event, ok := b.takeLocked(pending, now)
	b.mu.Unlock()
	if ok {
		b.emit(event)
	}
}

func (b *deltaBatcher) takeLocked(pending *pendingDelta, now time.Time) (transcript.Event, bool) {
	if pending.text.Len() == 0 || pending.event.Model == nil {
		return transcript.Event{}, false
	}
	if pending.timer != nil {
		pending.timer.Stop()
		pending.timer = nil
	}
	event := pending.event
	stream := *event.Model
	stream.Type = model.StreamTextDelta
	stream.Text = pending.text.String()
	event.Model = &stream
	pending.text.Reset()
	pending.lastFlush = now
	pending.emitted = true
	return event, true
}
