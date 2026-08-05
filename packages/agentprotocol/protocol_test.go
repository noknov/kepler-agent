package agentprotocol

import (
	"context"
	"testing"
)

func TestValidateAcceptsCompatibleMinorVersion(t *testing.T) {
	event := Event{Version: "1.99", Type: TurnStarted, ThreadID: "thread-1", TurnID: "turn-1"}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.Version = "2.0"
	if err := event.Validate(); err == nil {
		t.Fatal("expected incompatible major version to fail")
	}
}

func TestBrokerSequencesAndReplaysEvents(t *testing.T) {
	broker := NewBroker(8)
	broker.Publish(context.Background(), Event{Type: TurnStarted, ThreadID: "thread-1", TurnID: "turn-1"})
	broker.Publish(context.Background(), Event{Type: TurnCompleted, ThreadID: "thread-1", TurnID: "turn-1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx, "thread-1", 1)
	event := <-events
	if event.Sequence != 2 || event.Version != Version || event.ID == "" {
		t.Fatalf("unexpected replayed event: %#v", event)
	}
}

func TestValidateRequiresKnownEventPayloads(t *testing.T) {
	tests := []Event{
		{Type: TurnStarted, ThreadID: "thread-1"},
		{Type: ItemStarted, ThreadID: "thread-1", TurnID: "turn-1"},
		{Type: ErrorOccurred, ThreadID: "thread-1"},
	}
	for _, event := range tests {
		if err := event.Validate(); err == nil {
			t.Fatalf("expected validation failure for %#v", event)
		}
	}
	unknown := Event{Type: "extension.custom", ThreadID: "thread-1"}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("same-major extension event should be accepted: %v", err)
	}
}

func TestBrokerCanReplayHistoryLargerThanDefaultSubscriberBuffer(t *testing.T) {
	broker := NewBroker(defaultHistoryLimit + 1)
	for i := 0; i < defaultHistoryLimit+1; i++ {
		broker.Publish(context.Background(), Event{Type: ThreadStarted, ThreadID: "thread-1"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx, "thread-1", 0)
	for i := 0; i < defaultHistoryLimit+1; i++ {
		<-events
	}
}
