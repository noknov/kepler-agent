package web

import (
	"context"
	"sync"
)

type eventKind string

const (
	kindChunks  eventKind = "chunks"
	kindMessage eventKind = "message"
	kindDone    eventKind = "done"
)

type hubEvent struct {
	Kind   eventKind
	Chunks []map[string]any
	Text   string
}

// WebHub maintains a buffered channel per active web conversation, keyed by convID.
type WebHub struct {
	mu       sync.Mutex
	channels map[string]chan hubEvent
}

func NewHub() *WebHub {
	return &WebHub{channels: make(map[string]chan hubEvent)}
}

func (h *WebHub) register(convID string) <-chan hubEvent {
	ch := make(chan hubEvent, 256)
	h.mu.Lock()
	h.channels[convID] = ch
	h.mu.Unlock()
	return ch
}

func (h *WebHub) deregister(convID string) {
	h.mu.Lock()
	delete(h.channels, convID)
	h.mu.Unlock()
}

func (h *WebHub) send(key string, event hubEvent) {
	h.mu.Lock()
	ch, ok := h.channels[key]
	h.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- event:
	default:
	}
}

// HubMessenger implements conversation.Messenger and routes events through WebHub.
// It uses threadTS as the routing key so both progress and answer streams share
// the same SSE channel for a given conversation.
type HubMessenger struct {
	hub *WebHub
}

func NewHubMessenger(hub *WebHub) *HubMessenger {
	return &HubMessenger{hub: hub}
}

// StartStream returns threadTS as streamTS so all downstream AppendStream calls
// route to the same hub channel regardless of which internal stream they belong to.
func (m *HubMessenger) StartStream(_ context.Context, _, threadTS, _ string) (string, error) {
	return threadTS, nil
}

func (m *HubMessenger) AppendStream(_ context.Context, _, ts string, chunks []map[string]any) error {
	m.hub.send(ts, hubEvent{Kind: kindChunks, Chunks: chunks})
	return nil
}

func (m *HubMessenger) StopStream(_ context.Context, _, ts string) error {
	return nil
}

func (m *HubMessenger) PostMessage(_ context.Context, _, threadTS, text string) (string, error) {
	m.hub.send(threadTS, hubEvent{Kind: kindMessage, Text: text})
	return threadTS, nil
}

func (m *HubMessenger) DeleteMessage(_ context.Context, _, _ string) error {
	return nil
}

func (m *HubMessenger) ThreadContext(_ context.Context, _, _ string, _ int) string {
	return ""
}
