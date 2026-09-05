package transcript

import (
	"context"
	"testing"
	"time"
)

type recordingSink struct{ events chan Event }

func (s recordingSink) Publish(_ context.Context, event Event) { s.events <- event }

type blockingSink struct {
	started chan struct{}
	release <-chan struct{}
}

func (s blockingSink) Publish(_ context.Context, _ Event) {
	s.started <- struct{}{}
	<-s.release
}

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

func TestAsyncSinkShedsOverflowWithoutDetachedPublishers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	sink := NewAsyncSink(ctx, blockingSink{started: started, release: release}, 1)
	sink.Publish(context.Background(), Event{ID: "first", Type: TurnStarted})
	<-started // The worker is now occupied; the second event fills its queue.
	sink.Publish(context.Background(), Event{ID: "second", Type: TurnStarted})
	sink.Publish(context.Background(), Event{ID: "third", Type: TurnStarted})
	if got := sink.Dropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	close(release)
}
