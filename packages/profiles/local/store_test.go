package local

import (
	"context"
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/model"
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

func TestJSONLStoreAppendsForkBatchWithContiguousSequences(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	batch := []transcript.Event{
		{ID: "fork-1", SessionID: "child", Type: transcript.SessionStarted},
		{ID: "fork-2", SessionID: "child", Type: transcript.UserInput},
	}
	appended, err := store.AppendBatch(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(appended) != 2 || appended[0].Sequence != 1 || appended[1].Sequence != 2 {
		t.Fatalf("appended=%+v", appended)
	}
	loaded, err := store.Load(context.Background(), "child", 0)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestJSONLTurnWindowPreservesAuditAndRebuildsIndex(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	input := model.TextMessage(model.RoleUser, "original task")
	summary := model.TextMessage(model.RoleUser, "checkpoint")
	for _, event := range []transcript.Event{
		{Type: transcript.UserInput, Message: &input, TurnID: "old"},
		{Type: transcript.ModelCompleted, TurnID: "old"},
		{Type: transcript.CompactionCreated, Message: &summary, Metadata: json.RawMessage(`{"covers_through":2}`), TurnID: "old"},
	} {
		event.SessionID = "s"
		if _, err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh store must rebuild the same continuation metadata from disk.
	reopened, err := NewJSONLStore(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, reader := range []*JSONLStore{store, reopened} {
		events, err := reader.LoadTurn(ctx, "s", "new")
		if err != nil || len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 3 {
			t.Fatalf("window=%v err=%v", events, err)
		}
		full, err := reader.Load(ctx, "s", 0)
		if err != nil || len(full) != 3 {
			t.Fatalf("audit was changed: len=%d err=%v", len(full), err)
		}
		replay, err := reader.LoadTurn(ctx, "s", "old")
		if err != nil || len(replay) != 3 {
			t.Fatalf("replay accounting missing: len=%d err=%v", len(replay), err)
		}
	}
}
