package transcript

import (
	"context"
	"testing"
	"time"
)

type recordingSink struct{ events chan Event }

func (s recordingSink) Publish(_ context.Context, event Event) { s.events <- event }

func TestAsyncSinkForwardsCommittedEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorded := make(chan Event, 1)
	sink := NewAsyncSink(ctx, recordingSink{events: recorded}, 1)
	sink.Publish(context.Background(), Event{ID: "event-1", Type: TurnStarted})
	select {
	case event := <-recorded:
		if event.ID != "event-1" {
			t.Fatalf("event id = %q", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not forwarded")
	}
}

func TestAsyncSinkSkipsStreamDeltas(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorded := make(chan Event, 1)
	sink := NewAsyncSink(ctx, recordingSink{events: recorded}, 1)
	sink.Publish(context.Background(), Event{ID: "delta", Type: ModelStreamed})
	select {
	case event := <-recorded:
		t.Fatalf("unexpected event: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}
