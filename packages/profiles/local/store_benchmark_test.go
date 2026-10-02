package local

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func BenchmarkJSONLAppend(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			store, err := NewJSONLStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			message := model.TextMessage(model.RoleUser, strings.Repeat("x", 1024))
			events := make([]transcript.Event, size)
			for i := range events {
				events[i] = transcript.Event{ID: fmt.Sprint(i), SessionID: "bench", Message: &message}
			}
			if _, err := store.AppendBatch(context.Background(), events); err != nil {
				b.Fatal(err)
			}
			// Hydrate once, as a real RunTurn does before appending its events.
			if _, err := store.Load(context.Background(), "bench", 0); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := store.Append(context.Background(), transcript.Event{ID: fmt.Sprintf("new-%d", i), SessionID: "bench", Message: &message}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
