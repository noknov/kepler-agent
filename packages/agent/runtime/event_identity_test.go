package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type ackRetryInput struct {
	failed bool
	acked  bool
}

func (s *ackRetryInput) Claim(context.Context, int) ([]PendingInput, error) {
	if s.acked {
		return nil, nil
	}
	return []PendingInput{{ID: "same-source-id", Message: model.TextMessage(model.RoleUser, "steering evidence")}}, nil
}
func (s *ackRetryInput) Ack(context.Context, string) error {
	if !s.failed {
		s.failed = true
		return errors.New("ack unavailable after append")
	}
	s.acked = true
	return nil
}

func TestStableEventIdentityIncludesItsOwner(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
	seen := make(map[string]bool)
	for _, owner := range []struct{ session, turn string }{
		{"session", "same-turn"}, {"another-session", "same-turn"},
		{"a:b", "c"}, {"a", "b:c"},
	} {
		event := transcript.Event{ID: "logical-event", SessionID: owner.session, TurnID: owner.turn, Type: transcript.StepStarted}
		first, err := runner.record(t.Context(), event)
		if err != nil {
			t.Fatal(err)
		}
		if seen[first.ID] {
			t.Fatalf("distinct owners share a durable ID: %+v", owner)
		}
		seen[first.ID] = true
		again, err := runner.record(t.Context(), event)
		if err != nil || again.ID != first.ID || again.Sequence != first.Sequence {
			t.Fatalf("same logical event lost idempotency: first=%+v again=%+v err=%v", first, again, err)
		}
	}
}

func TestSteeringRedeliveryKeepsOneEventPerOwner(t *testing.T) {
	store := transcript.NewMemoryStore()
	seen := make(map[string]bool)
	for _, session := range []string{"first-owner", "second-owner"} {
		catalog, _ := tool.NewCatalog()
		runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
		request := TurnRequest{SessionID: session, TurnID: "same-turn", Steering: &ackRetryInput{}}
		if err := runner.appendSteering(t.Context(), request); err == nil {
			t.Fatal("expected append-before-ack interruption")
		}
		if err := runner.appendSteering(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		events, err := store.Load(t.Context(), session, 0)
		if err != nil || len(events) != 1 || events[0].Type != transcript.SteeringInput || events[0].Message.Text() != "steering evidence" || seen[events[0].ID] {
			t.Fatalf("steering lost its owner or deduplication: events=%+v err=%v", events, err)
		}
		seen[events[0].ID] = true
	}
}
