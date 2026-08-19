package runtime

import "context"

// ConnectionContinuation is a transport-neutral request to resume a session
// after an external credential has been granted.
type ConnectionContinuation struct {
	UserID    string
	Provider  string
	SessionID string
	Channel   string
	ThreadTS  string
}

// ConnectionContinuationStore is implemented by product adapters. The shared
// runtime does not know whether a connection comes from Slack, CLI, or another
// surface.
type ConnectionContinuationStore interface {
	Save(context.Context, ConnectionContinuation) error
}
