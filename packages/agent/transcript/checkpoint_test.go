package transcript

import (
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"reflect"
	"testing"
)

func TestTurnWindowPreservesReplayAccountingAndOriginalInput(t *testing.T) {
	input := model.TextMessage(model.RoleUser, "original task")
	summary := model.TextMessage(model.RoleUser, "checkpoint")
	events := []Event{
		{Sequence: 1, TurnID: "old", Type: UserInput, Message: &input},
		{Sequence: 2, TurnID: "old", Type: ModelCompleted},
		{Sequence: 3, TurnID: "old", Type: TurnCompleted},
		{Sequence: 4, TurnID: "old", Type: CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":3}`)},
		{Sequence: 5, TurnID: "other", Type: CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":999}`)},
	}
	for _, test := range []struct {
		turn string
		want []uint64
	}{{"new", []uint64{1, 4, 5}}, {"old", []uint64{1, 2, 3, 4, 5}}} {
		var got []uint64
		for _, event := range TurnWindow(events, test.turn) {
			got = append(got, event.Sequence)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("turn=%s got=%v want=%v", test.turn, got, test.want)
		}
	}
}
