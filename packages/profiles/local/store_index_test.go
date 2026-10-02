package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestJSONLIndexRefreshesExternalAppendAndReplacement(t *testing.T) {
	ctx := context.Background()
	first, _ := NewJSONLStore(t.TempDir())
	second, _ := NewJSONLStore(first.Root)
	for i, store := range []*JSONLStore{first, second, first, second} {
		event, err := store.Append(ctx, transcript.Event{ID: fmt.Sprint(i), SessionID: "shared"})
		if err != nil || event.Sequence != uint64(i+1) {
			t.Fatalf("append %d: %+v %v", i, event, err)
		}
	}
	// Batch writes replace the file atomically. A warm index must notice it.
	if _, err := second.AppendBatch(ctx, []transcript.Event{{ID: "batch", SessionID: "shared"}}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := first.Append(ctx, transcript.Event{ID: "batch", SessionID: "shared", Error: "should not replace"})
	if err != nil || duplicate.Sequence != 5 || duplicate.Error != "" {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	events, err := first.Load(ctx, "shared", 3)
	if err != nil || len(events) != 2 || events[0].Sequence != 4 {
		t.Fatalf("incremental load = %+v, %v", events, err)
	}
	// In-place truncation invalidates cached offsets and IDs.
	if err := os.Truncate(filepath.Join(first.Root, "shared", "events.jsonl"), 0); err != nil {
		t.Fatal(err)
	}
	event, err := first.Append(ctx, transcript.Event{ID: "batch", SessionID: "shared"})
	if err != nil || event.Sequence != 1 {
		t.Fatalf("after truncate: %+v %v", event, err)
	}
}

func TestJSONLRepairsValidTailWithoutNewline(t *testing.T) {
	store, _ := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	original := transcript.Event{ID: "first", SessionID: "tail", Sequence: 1}
	_, err := store.Append(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, "tail", "events.jsonl")
	data, _ := json.Marshal(original)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	appended, err := store.Append(ctx, transcript.Event{ID: "second", SessionID: "tail"})
	if err != nil || appended.Sequence != 2 {
		t.Fatalf("append: %+v, %v", appended, err)
	}
	events, err := store.Load(ctx, "tail", 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("load: %+v, %v", events, err)
	}
}

func TestJSONLWaitForBusySessionCanBeCanceled(t *testing.T) {
	store, _ := NewJSONLStore(t.TempDir())
	directory := filepath.Join(store.Root, "busy")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockSessionFile(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = store.Append(ctx, transcript.Event{SessionID: "busy"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("append: %v", err)
	}
	// A busy session must not prevent another session from making progress.
	if _, err := store.Append(context.Background(), transcript.Event{SessionID: "other"}); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLConcurrentWritersKeepContiguousSequence(t *testing.T) {
	store, _ := NewJSONLStore(t.TempDir())
	other, _ := NewJSONLStore(store.Root)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writer := store
			if i%2 == 0 {
				writer = other
			}
			if _, err := writer.Append(context.Background(), transcript.Event{ID: fmt.Sprint(i), SessionID: "shared"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	events, err := store.Load(context.Background(), "shared", 0)
	if err != nil || len(events) != 24 {
		t.Fatalf("load: count=%d err=%v", len(events), err)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence at %d = %d", i, event.Sequence)
		}
	}
}

func TestJSONLRejectsCompleteCorruptionAndRollsBackInvalidBatch(t *testing.T) {
	store, _ := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	_, _ = store.Append(ctx, transcript.Event{SessionID: "session", ID: "existing"})
	_, err := store.AppendBatch(ctx, []transcript.Event{{SessionID: "session", ID: "good"}, {SessionID: "session", Metadata: json.RawMessage(`invalid`)}})
	if err == nil {
		t.Fatal("invalid batch accepted")
	}
	events, err := store.Load(ctx, "session", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("partial batch visible: %d, %v", len(events), err)
	}
	file, err := os.OpenFile(filepath.Join(store.Root, "session", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("invalid\n")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "session", 0); err == nil {
		t.Fatal("complete corrupted record was silently discarded")
	}
}
