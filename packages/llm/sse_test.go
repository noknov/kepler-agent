package llm

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
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

func TestReadSSEPreservesFieldSemantics(t *testing.T) {
	for _, test := range []struct{ name, wire, event, data string }{
		{"last event field wins", "event: old\nevent: final\ndata: ok\n\n", "final", "ok"},
		{"spaces are data", "data:  keep  \n\n", "", " keep  "},
		{"empty data line", "data\n\n", "", ""},
		{"empty multiline data", "data:\ndata: end\n\n", "", "\nend"},
		{"CR delimiter", "event: final\rdata: ok\r\r", "final", "ok"},
		{"UTF8 BOM", "\xef\xbb\xbfevent: final\r\ndata: ok\r\n\r\n", "final", "ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, reader := range []io.Reader{strings.NewReader(test.wire), iotest.OneByteReader(strings.NewReader(test.wire))} {
				var events []sseEvent
				err := readSSE(reader, func(event sseEvent) bool { events = append(events, event); return true })
				if err != nil || len(events) != 1 || events[0].Event != test.event || events[0].Data != test.data {
					t.Fatalf("events=%q err=%v", events, err)
				}
			}
		})
	}
}

type sseTailReader struct{ reads int }

func (r *sseTailReader) Read([]byte) (int, error) { r.reads++; return 0, io.ErrUnexpectedEOF }

func TestReadSSEStopsAtBareCRBoundary(t *testing.T) {
	tail := &sseTailReader{}
	err := readSSE(io.MultiReader(strings.NewReader("data: done\r\r"), tail), func(event sseEvent) bool { return false })
	if err != nil || tail.reads != 0 {
		t.Fatalf("read beyond dispatched CR event: reads=%d err=%v", tail.reads, err)
	}
}

func TestReadSSEConsumesAllBufferedCRLFEvents(t *testing.T) {
	for _, suffix := range []string{"", "event: final\r\ndata: third\r\n\r\n"} {
		wire := "data: first\r\n\r\ndata: second\r\n\r\n" + suffix
		var events []sseEvent
		err := readSSE(strings.NewReader(wire), func(event sseEvent) bool { events = append(events, event); return true })
		want := 2
		if suffix != "" {
			want++
		}
		if err != nil || len(events) != want || events[0].Data != "first" || events[1].Data != "second" {
			t.Fatalf("buffered CRLF events lost: events=%q err=%v", events, err)
		}
	}
	tail := &sseTailReader{}
	calls := 0
	err := readSSE(io.MultiReader(strings.NewReader("data: first\r\n\r\ndata: terminal\r\n\r\n"), tail), func(event sseEvent) bool { calls++; return event.Data != "terminal" })
	if err != nil || calls != 2 || tail.reads != 0 {
		t.Fatalf("buffered terminal waited for network read: calls=%d reads=%d err=%v", calls, tail.reads, err)
	}
}

func TestReadSSEBoundsWholeEvent(t *testing.T) {
	for _, extra := range []string{"", "data: x\n"} {
		wire := "data:" + strings.Repeat("a", maxSSEEventBytes/2) + "\ndata:" + strings.Repeat("b", maxSSEEventBytes/2-1) + "\n" + extra + "\n"
		calls := 0
		err := readSSE(strings.NewReader(wire), func(event sseEvent) bool {
			calls++
			if len(event.Data) != maxSSEEventBytes {
				t.Errorf("data bytes=%d", len(event.Data))
			}
			return true
		})
		if extra == "" {
			if err != nil || calls != 1 {
				t.Fatalf("exact event budget rejected: calls=%d err=%v", calls, err)
			}
		} else {
			var protocol ProtocolError
			if !errors.As(err, &protocol) || calls != 0 {
				t.Fatalf("oversized event dispatched: calls=%d err=%v", calls, err)
			}
		}
	}
}

func BenchmarkReadSSE(b *testing.B) {
	wire := strings.Repeat("event: delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n", 64)
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	for range b.N {
		if err := readSSE(strings.NewReader(wire), func(sseEvent) bool { return true }); err != nil {
			b.Fatal(err)
		}
	}
}
