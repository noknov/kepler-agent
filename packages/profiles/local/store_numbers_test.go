package local

import (
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestJSONLTranscriptPreservesMetadataNumbers(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := transcript.Event{ID: "event", SessionID: "session", TurnID: "turn", Type: transcript.ToolCallCompleted,
		ToolResult: &tool.Result{Metadata: map[string]any{"ids": []any{json.Number("9007199254740993"), json.Number("9223372036854775807")}}},
	}
	if _, err := store.Append(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(original.ToolResult.Metadata)
	for _, load := range []struct {
		name string
		read func() (transcript.Event, error)
	}{
		{"load", func() (transcript.Event, error) {
			events, err := store.Load(t.Context(), "session", 0)
			return events[0], err
		}},
		{"load turn", func() (transcript.Event, error) {
			events, err := store.LoadTurn(t.Context(), "session", "turn")
			return events[0], err
		}},
		{"read event", func() (transcript.Event, error) { return store.ReadEvent(t.Context(), "session", 1) }},
		{"duplicate append", func() (transcript.Event, error) { return store.Append(t.Context(), original) }},
	} {
		t.Run(load.name, func(t *testing.T) {
			event, err := load.read()
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(event.ToolResult.Metadata)
			if string(got) != string(want) {
				t.Fatalf("metadata integers rounded: got=%s want=%s", got, want)
			}
		})
	}
}
