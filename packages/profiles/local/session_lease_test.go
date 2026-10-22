package local

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestSessionTurnLeaseSpansStoreInstancesWithoutBlockingAppend(t *testing.T) {
	root := t.TempDir()
	first, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.Lock(context.Background(), "session:s")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := first.Append(ctx, transcript.Event{SessionID: "s", Type: transcript.SessionStarted}); err != nil {
		t.Fatalf("turn lock blocks transcript append: %v", err)
	}
	if unlock, err := second.Lock(ctx, "session:s"); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("second owner admitted: %v", err)
	}
	other, err := second.Lock(context.Background(), "session:other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	release()
	next, err := second.Lock(context.Background(), "session:s")
	if err != nil {
		t.Fatal(err)
	}
	next()
	if _, err := first.Lock(context.Background(), "session:../escape"); err == nil {
		t.Fatal("invalid session accepted")
	}
}
