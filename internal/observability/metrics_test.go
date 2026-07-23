package observability

import "testing"

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
