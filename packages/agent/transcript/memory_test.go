package transcript

import (
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

func snapshotFixture() Event {
	return Event{ID: "event", SessionID: "session", TurnID: "turn", Type: ApprovalRequested,
		ToolCall:   &tool.Call{Arguments: json.RawMessage(`{"target":"original"}`), State: json.RawMessage(`{"proof":"captured"}`), Scope: tool.Scope{Values: map[string]string{"owner": "original"}}},
		Message:    &model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentText, Text: "original"}}},
		ToolResult: &tool.Result{Metadata: map[string]any{"integer": json.Number("9007199254740993")}},
	}
}

func corruptSnapshot(event Event) {
	event.ToolCall.Arguments[0] = '!'
	event.ToolCall.State[0] = '!'
	event.ToolCall.Scope.Values["owner"] = "changed"
	event.Message.Content[0].Text = "changed"
	event.ToolResult.Metadata["integer"] = 0
}

func TestMemoryStoreOwnsAppendAndReadSnapshots(t *testing.T) {
	for _, mode := range []string{"append", "batch", "append return", "batch return", "duplicate return", "load", "load turn", "read event"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			original := snapshotFixture()
			var returned Event
			var err error
			if mode == "batch" || mode == "batch return" {
				var batch []Event
				batch, err = store.AppendBatch(t.Context(), []Event{original})
				if err == nil {
					returned = batch[0]
				}
			} else {
				returned, err = store.Append(t.Context(), original)
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := snapshotFixture()
			expected.Sequence = 1
			switch mode {
			case "append", "batch":
				corruptSnapshot(original)
			case "append return", "batch return":
				corruptSnapshot(returned)
			case "duplicate return":
				returned, err = store.Append(t.Context(), Event{ID: original.ID, SessionID: original.SessionID, TurnID: original.TurnID})
				if err == nil {
					corruptSnapshot(returned)
				}
			case "read event":
				returned, err = store.ReadEvent(t.Context(), "session", 1)
				if err == nil {
					corruptSnapshot(returned)
				}
			default:
				var events []Event
				if mode == "load turn" {
					events, err = store.LoadTurn(t.Context(), "session", "turn")
				} else {
					events, err = store.Load(t.Context(), "session", 0)
				}
				if err == nil {
					corruptSnapshot(events[0])
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.ReadEvent(t.Context(), "session", 1)
			want, _ := json.Marshal(expected)
			got, marshalErr := json.Marshal(stored)
			if err != nil || marshalErr != nil || string(got) != string(want) {
				t.Fatalf("external mutation changed canonical history: stored=%s expected=%s read=%v marshal=%v", got, want, err, marshalErr)
			}
		})
	}
}

func TestMemoryStoreRejectsInvalidBatchAtomically(t *testing.T) {
	store := NewMemoryStore()
	invalid := snapshotFixture()
	invalid.ID = "invalid"
	invalid.Metadata = json.RawMessage(`invalid`)
	if _, err := store.AppendBatch(t.Context(), []Event{snapshotFixture(), invalid}); err == nil {
		t.Fatal("accepted nonserializable canonical event")
	}
	events, err := store.Load(t.Context(), "session", 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed batch changed history: events=%+v err=%v", events, err)
	}
}

func TestMemoryStoreDuplicateCannotChangeTurnOwner(t *testing.T) {
	store := NewMemoryStore()
	original := snapshotFixture()
	if _, err := store.Append(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	original.TurnID = "another-turn"
	if event, err := store.Append(t.Context(), original); err == nil || event.ID != "" {
		t.Fatalf("duplicate returned another turn's fact: event=%+v err=%v", event, err)
	}
}
