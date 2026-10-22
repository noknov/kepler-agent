package transcript

import (
	"context"
	"fmt"
)

// ReadAt is scoped by its caller. It never treats a sequence as authority to
// select a different session, and uses indexed access when the store supports it.
func ReadAt(ctx context.Context, store Store, session string, sequence uint64) (Event, error) {
	if session == "" || sequence == 0 {
		return Event{}, fmt.Errorf("session and sequence are required")
	}
	if reader, ok := store.(EventReader); ok {
		return reader.ReadEvent(ctx, session, sequence)
	}
	events, err := store.Load(ctx, session, sequence-1)
	if err != nil {
		return Event{}, err
	}
	for _, event := range events {
		if event.Sequence == sequence {
			return event, nil
		}
	}
	return Event{}, fmt.Errorf("event not found")
}
