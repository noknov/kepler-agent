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

func TestSessionEventsRetainOutOfOrderDurableAppends(t *testing.T) {
	state := &sessionEvents{requests: make(map[string]map[string]bool)}
	input := model.TextMessage(model.RoleUser, "task")
	state.append(transcript.Event{Sequence: 1, Type: transcript.UserInput, Message: &input})
	summary := model.TextMessage(model.RoleUser, "checkpoint")
	state.append(transcript.Event{Sequence: 5, Type: transcript.CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":3}`)})
	oldEvents := state.events[:len(state.events):len(state.events)]
	oldContext := state.context[:len(state.context):len(state.context)]
	// The store committed these before sequence 5, but their callers resumed later.
	request := transcript.Event{Sequence: 2, TurnID: "t", Type: transcript.ModelRequestStarted, Metadata: json.RawMessage(`{"request_id":"r1"}`)}
	state.append(request)
	state.append(transcript.Event{Sequence: 3, Type: transcript.TaskStateUpdated, Metadata: json.RawMessage(`{"schema_version":1,"revision":1,"items":[]}`)})
	answer := model.TextMessage(model.RoleAssistant, "after covered history")
	state.append(transcript.Event{Sequence: 4, Type: transcript.AssistantMessage, Message: &answer})
	state.append(request) // Store idempotency must still avoid duplicate accounting.
	if len(state.events) != 5 || len(state.requests["t"]) != 1 {
		t.Fatalf("lost/duplicated durable facts: events=%+v requests=%v", state.events, state.requests)
	}
	for i, event := range state.events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("noncanonical order: %+v", state.events)
		}
	}
	want := []uint64{1, 3, 4, 5}
	if len(state.context) != len(want) {
		t.Fatalf("derived context=%+v", state.context)
	}
	for i, sequence := range want {
		if state.context[i].Sequence != sequence {
			t.Fatalf("checkpoint lost retained input/state or post-checkpoint evidence: %+v", state.context)
		}
	}
	if len(oldEvents) != 2 || oldEvents[1].Sequence != 5 || len(oldContext) != 2 || oldContext[1].Sequence != 5 {
		t.Fatal("reordering mutated an already published view")
	}
}
