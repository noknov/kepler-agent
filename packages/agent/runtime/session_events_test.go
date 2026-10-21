package runtime

import (
	"context"
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"testing"
)

func BenchmarkContextEventsWithLongAuditTrail(b *testing.B) {
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	events := make([]transcript.Event, 10000)
	for i := range events {
		events[i] = transcript.Event{Sequence: uint64(i + 1), Type: transcript.ContextProjected}
	}
	input := model.TextMessage(model.RoleUser, "task")
	events = append(events, transcript.Event{Sequence: 10001, Type: transcript.UserInput, Message: &input})
	runner.setEventCache("s", events)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		history, err := runner.contextEvents(context.Background(), "s")
		if err != nil || len(history) != 1 {
			b.Fatal(err)
		}
	}
}

func TestSessionEventViewsAreAppendSafeAndRequestIDsIncremental(t *testing.T) {
	state := &sessionEvents{requests: make(map[string]map[string]bool)}
	for i, attempt := range []int{1, 2} {
		raw, _ := json.Marshal(modelRequestState{RequestID: "request", Attempt: attempt})
		state.append(transcript.Event{ID: "event", Sequence: uint64(i + 1), TurnID: "t", Type: transcript.ModelRequestStarted, Metadata: raw})
	}
	if len(state.requests["t"]) != 1 {
		t.Fatal("retry consumed another logical request ID")
	}
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	runner.eventCache["s"] = state
	view, _ := runner.turnEvents(context.Background(), "s")
	if cap(view) != len(view) {
		t.Fatal("view append could modify live cache")
	}
	state.append(transcript.Event{Sequence: 3, Type: transcript.ContextProjected})
	if len(view) != 2 {
		t.Fatal("immutable view changed after append")
	}
	id, err := runner.nextModelRequestID(context.Background(), TurnRequest{SessionID: "s", TurnID: "t"})
	if err != nil || id != "t:model:2" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}
