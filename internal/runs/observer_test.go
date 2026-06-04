package runs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/observability"
)

func TestObserverRecordsTraceMetadataAndStack(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	observer := NewObserver(store, Run{Model: "test-model"}, observability.CostRates{})
	observer.ToolCallWithMetadata("code-read_file", json.RawMessage(`{"path":"app.go","max_lines":20}`), time.Millisecond, nil)
	observer.RecordErrorStack("stack trace")
	observer.Finish("error", "err-1", assertErr("boom"), "")

	run, ok, err := store.Get(context.Background(), observer.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected run to be saved")
	}
	if run.TraceID == "" || len(run.Steps) != 1 || run.Steps[0].SpanID == "" || run.Steps[0].ParentSpanID != run.TraceID {
		t.Fatalf("missing trace metadata: %#v", run)
	}
	if run.ErrorStack != "stack trace" {
		t.Fatalf("ErrorStack = %q", run.ErrorStack)
	}
	if run.Steps[0].Metadata["args_hash"] == "" {
		t.Fatalf("missing args hash: %#v", run.Steps[0].Metadata)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
