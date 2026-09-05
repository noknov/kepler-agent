package observability

import (
	"errors"
	"testing"
	"time"
)

func TestRecorderTracksAgentEvents(t *testing.T) {
	rec := NewRecorder()
	rec.Event("context_compact", map[string]any{"layer": "auto"})
	rec.Event("compact_error", map[string]any{"error": "summary failed"})

	snap := rec.Snapshot()
	if snap.AgentEvents["context_compact"] != 1 {
		t.Fatalf("context_compact count = %d, want 1", snap.AgentEvents["context_compact"])
	}
	if snap.AgentEvents["compact_error"] != 1 {
		t.Fatalf("compact_error count = %d, want 1", snap.AgentEvents["compact_error"])
	}
	found := false
	for _, err := range snap.LastErrors {
		if err == "compact_error: summary failed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("LastErrors = %#v, want compact error", snap.LastErrors)
	}
}

func TestRecorderAddsKnownEventCount(t *testing.T) {
	rec := NewRecorder()
	rec.AddEvent("async_sink_dropped", 3, nil)
	rec.AddEvent("async_sink_dropped", 2, nil)

	if got := rec.Snapshot().AgentEvents["async_sink_dropped"]; got != 5 {
		t.Fatalf("async_sink_dropped = %d, want 5", got)
	}
}

func TestRecorderReportsPercentilesAndPreservesErrorOrder(t *testing.T) {
	rec := NewRecorder()
	for _, latency := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond} {
		rec.Latency(latency)
	}
	rec.Error(errors.New("first"))
	rec.Error(errors.New("second"))
	snap := rec.Snapshot()
	if snap.LatencyMS.P50 != 2 || snap.LatencyMS.P95 != 100 || snap.LatencyMS.P99 != 100 {
		t.Fatalf("latency percentiles = %#v", snap.LatencyMS)
	}
	if len(snap.LastErrors) != 2 || snap.LastErrors[0] != "first" || snap.LastErrors[1] != "second" {
		t.Fatalf("LastErrors lost chronology: %#v", snap.LastErrors)
	}
}
