package runtime

import (
	"context"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// ConnectionContinuation is a transport-neutral request to resume a session
// after an external credential has been granted.
type ConnectionContinuation struct {
	Provider   string
	InstanceID string
	TurnID     string
	Scope      tool.Scope
}

// ConnectionContinuationStore is implemented by product adapters. The shared
// runtime does not know whether a connection comes from Slack, CLI, or another
// surface.
type ConnectionContinuationStore interface {
	Save(context.Context, ConnectionContinuation) error
}
