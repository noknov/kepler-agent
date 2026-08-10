package session

import (
	"context"
	"encoding/base64"
)

// Locker is implemented by durable stores that can serialize an entire turn.
// The PostgreSQL implementation uses advisory locks, which work across pods.
type Locker interface {
	Lock(ctx context.Context, id string) (unlock func(), err error)
}

func ID(channel, threadTS string) string {
	raw := channel + ":" + threadTS
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
