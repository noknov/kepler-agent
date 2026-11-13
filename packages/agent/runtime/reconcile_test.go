package runtime

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestPresentationReconciliationIgnoresActiveAndSupersededTurns(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	r, err := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	applyCount := 0
	apply := func(context.Context, TerminationReason) error { applyCount++; return nil }
	for _, event := range []transcript.Event{{Type: transcript.TurnStarted, TurnID: "old"}, {Type: transcript.TurnCompleted, TurnID: "old", Status: string(TerminationCompleted)}, {Type: transcript.TurnStarted, TurnID: "new"}} {
		event.SessionID = "s"
		if _, err := store.Append(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	if settled, err := r.ReconcileTurn(t.Context(), "s", "old", apply); err != nil || !settled || applyCount != 0 {
		t.Fatalf("old settled=%v count=%d error=%v", settled, applyCount, err)
	}
	if settled, err := r.ReconcileTurn(t.Context(), "s", "new", apply); err != nil || settled || applyCount != 0 {
		t.Fatalf("active settled=%v count=%d error=%v", settled, applyCount, err)
	}
	store.Append(t.Context(), transcript.Event{SessionID: "s", TurnID: "new", Type: transcript.TurnCompleted, Status: string(TerminationPendingInput)})
	if settled, err := r.ReconcileTurn(t.Context(), "s", "new", apply); err != nil || !settled || applyCount != 1 {
		t.Fatalf("terminal settled=%v count=%d error=%v", settled, applyCount, err)
	}
}
