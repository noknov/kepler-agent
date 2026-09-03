package appserver

import (
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestDeltaBatcherDeliversFirstDeltaImmediately(t *testing.T) {
	emitted := make(chan transcript.Event, 2)
	batcher := newDeltaBatcher(time.Hour, 1024, func(event transcript.Event) { emitted <- event })
	batcher.push(streamEvent("turn_1", "hello"))

	select {
	case event := <-emitted:
		if got := event.Model.Text; got != "hello" {
			t.Fatalf("first delta = %q, want hello", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first delta was not delivered immediately")
	}
}

func TestDeltaBatcherCoalescesAndFlushesTrailingText(t *testing.T) {
	emitted := make(chan transcript.Event, 2)
	batcher := newDeltaBatcher(time.Hour, 1024, func(event transcript.Event) { emitted <- event })
	batcher.push(streamEvent("turn_1", "a"))
	<-emitted // First token prioritizes time to first text.
	batcher.push(streamEvent("turn_1", "b"))
	batcher.push(streamEvent("turn_1", "c"))
	batcher.flushTurn("turn_1")

	select {
	case event := <-emitted:
		if got := event.Model.Text; got != "bc" {
			t.Fatalf("flushed delta = %q, want bc", got)
		}
	case <-time.After(time.Second):
		t.Fatal("trailing delta was not flushed")
	}
}

func TestDeltaBatcherFlushesOnCadence(t *testing.T) {
	emitted := make(chan transcript.Event, 2)
	batcher := newDeltaBatcher(10*time.Millisecond, 1024, func(event transcript.Event) { emitted <- event })
	batcher.push(streamEvent("turn_1", "a"))
	<-emitted
	batcher.push(streamEvent("turn_1", "b"))

	select {
	case event := <-emitted:
		if got := event.Model.Text; got != "b" {
			t.Fatalf("timed delta = %q, want b", got)
		}
	case <-time.After(time.Second):
		t.Fatal("delta did not flush on cadence")
	}
}

func streamEvent(turnID, text string) transcript.Event {
	return transcript.Event{
		TurnID: turnID,
		Model:  &model.StreamEvent{Type: model.StreamTextDelta, Text: text},
	}
}
