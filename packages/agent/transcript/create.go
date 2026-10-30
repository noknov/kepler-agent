package transcript

import (
	"context"
	"errors"
)

var ErrSessionExists = errors.New("destination session already has canonical facts")

// SessionCreator commits initial facts only when the destination is empty.
// The emptiness check and batch commit share the store's ownership boundary.
// An empty batch checks emptiness but does not reserve a session ID.
type SessionCreator interface {
	Store
	CreateSession(context.Context, string, []Event) ([]Event, error)
}
