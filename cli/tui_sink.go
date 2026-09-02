package cli

import (
	"context"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// forwardSink routes agent transcript events into the alt-screen TUI.
type forwardSink struct {
	mu   sync.Mutex
	send func(agentEventMsg)
}

func newForwardSink() *forwardSink {
	return &forwardSink{}
}

func (f *forwardSink) attach(send func(agentEventMsg)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.send = send
}

func (f *forwardSink) Publish(_ context.Context, event transcript.Event) {
	f.mu.Lock()
	send := f.send
	f.mu.Unlock()
	if send != nil {
		send(agentEventMsg{event: event})
	}
}
