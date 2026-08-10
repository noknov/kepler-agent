package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
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
}
