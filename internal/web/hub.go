package web

import (
	"context"
	"strings"
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

// WebHub maintains a buffered channel per active web conversation.
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

func webHubKey(channel, threadTS string) string {
	userID := strings.TrimPrefix(channel, "web:")
	if userID == "" || userID == channel {
		return threadTS
	}
	return userID + ":" + threadTS
}

// HubMessenger implements conversation.Messenger and routes events through WebHub.
// It uses a user-scoped routing key so identical convIDs from different users
// cannot share the same SSE channel.
type HubMessenger struct {
	hub *WebHub
}

func NewHubMessenger(hub *WebHub) *HubMessenger {
	return &HubMessenger{hub: hub}
}

// StartStream returns the route key as streamTS so downstream AppendStream calls
// stay scoped to this user conversation.
func (m *HubMessenger) StartStream(_ context.Context, channel, threadTS, _ string) (string, error) {
	return webHubKey(channel, threadTS), nil
}

func (m *HubMessenger) AppendStream(_ context.Context, _, ts string, chunks []map[string]any) error {
	m.hub.send(ts, hubEvent{Kind: kindChunks, Chunks: chunks})
	return nil
}

func (m *HubMessenger) StopStream(_ context.Context, _, ts string) error {
	return nil
}

func (m *HubMessenger) PostMessage(_ context.Context, channel, threadTS, text string) (string, error) {
	key := webHubKey(channel, threadTS)
	m.hub.send(key, hubEvent{Kind: kindMessage, Text: text})
	return key, nil
}

func (m *HubMessenger) DeleteMessage(_ context.Context, _, _ string) error {
	return nil
}

func (m *HubMessenger) ThreadContext(_ context.Context, _, _ string, _ int) string {
	return ""
}
