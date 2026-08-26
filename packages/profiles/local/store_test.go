package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestJSONLStorePersistsAndIgnoresPartialTail(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if _, err := store.Append(context.Background(), transcript.Event{SessionID: "session-1", Type: transcript.UserInput}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(store.Root, "session-1", "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(`{"sequence":`); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(context.Background(), "session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Sequence != 2 {
		t.Fatalf("events = %#v", events)
	}
	appended, err := store.Append(context.Background(), transcript.Event{SessionID: "session-1", Type: transcript.UserInput})
	if err != nil || appended.Sequence != 3 {
		t.Fatalf("append after repair = %+v, %v", appended, err)
	}
	events, err = store.Load(context.Background(), "session-1", 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("events after repair = %#v, %v", events, err)
	}
}

func TestJSONLStoresCoordinateSequenceAcrossInstances(t *testing.T) {
	root := t.TempDir()
	first, _ := NewJSONLStore(root)
	second, _ := NewJSONLStore(root)
	if _, err := first.Append(context.Background(), transcript.Event{SessionID: "shared", Type: transcript.UserInput}); err != nil {
		t.Fatal(err)
	}
	event, err := second.Append(context.Background(), transcript.Event{SessionID: "shared", Type: transcript.UserInput})
	if err != nil || event.Sequence != 2 {
		t.Fatalf("second append = %+v, %v", event, err)
	}
}
