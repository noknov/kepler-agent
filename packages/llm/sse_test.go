package llm

import (
	"strings"
	"testing"
)

func TestReadSSEFlushesFinalEventAtEOF(t *testing.T) {
	var events []sseEvent
	err := readSSE(strings.NewReader("event: completed\ndata: {\"ok\":true}"), func(event sseEvent) bool {
		events = append(events, event)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "completed" || events[0].Data != `{"ok":true}` {
		t.Fatalf("events = %+v", events)
	}
}
