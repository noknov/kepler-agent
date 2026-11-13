package runtime

import (
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestOnlyMatchedSuccessfulInferenceAdvancesInputDelivery(t *testing.T) {
	state := sessionEvents{requestBoundaries: make(map[string]map[string]uint64)}
	state.append(transcript.Event{Sequence: 1, TurnID: "turn", Type: transcript.UserInput})
	state.append(transcript.Event{Sequence: 2, TurnID: "turn", Type: transcript.ModelRequestStarted, Metadata: json.RawMessage(`{"request_id":"r"}`)})
	state.append(transcript.Event{Sequence: 3, TurnID: "turn", Type: transcript.SteeringInput})
	if state.sampledThrough != 0 {
		t.Fatal("admission was mistaken for delivery")
	}
	state.append(transcript.Event{Sequence: 4, TurnID: "turn", Type: transcript.ModelCompleted, Metadata: json.RawMessage(`{"request_id":"r","finish_reason":"stop","input_through":1}`)})
	if state.sampledThrough != 1 {
		t.Fatal("completion consumed steering that arrived after admission")
	}
	state.append(transcript.Event{Sequence: 5, TurnID: "turn", Type: transcript.ModelRequestStarted, Metadata: json.RawMessage(`{"request_id":"failed"}`)})
	state.append(transcript.Event{Sequence: 6, TurnID: "turn", Type: transcript.ModelFailed, Metadata: json.RawMessage(`{"request_id":"failed"}`)})
	state.append(transcript.Event{Sequence: 7, TurnID: "turn", Type: transcript.CompactionCreated})
	if state.sampledThrough != 1 {
		t.Fatal("failure or summary was mistaken for input delivery")
	}
	state.append(transcript.Event{Sequence: 8, TurnID: "another-turn", Type: transcript.ModelRequestStarted, Metadata: json.RawMessage(`{"request_id":"legacy"}`)})
	state.append(transcript.Event{Sequence: 9, TurnID: "turn", Type: transcript.ModelCompleted, Metadata: json.RawMessage(`{"request_id":"legacy","finish_reason":"stop"}`)})
	if state.sampledThrough != 1 {
		t.Fatal("another turn's admission was used as delivery evidence")
	}
	state.append(transcript.Event{Sequence: 10, TurnID: "turn", Type: transcript.ModelRequestStarted, Metadata: json.RawMessage(`{"request_id":"old-format"}`)})
	state.append(transcript.Event{Sequence: 11, TurnID: "turn", Type: transcript.ModelCompleted, Metadata: json.RawMessage(`{"request_id":"old-format","finish_reason":"tool_calls"}`)})
	if state.sampledThrough != 9 {
		t.Fatal("matched legacy lifecycle evidence was not restored")
	}
}
