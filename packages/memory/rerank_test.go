package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func TestModelRankerPacksLargestPrefixWithinExactByteLimit(t *testing.T) {
	records := []Record{
		{Key: "first", Content: "中文🙂\n\"<&", UpdatedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
		{Key: "second", Content: strings.Repeat("evidence", 10)},
		{Key: "third", Content: "last"},
	}
	query := "query <&\n🙂"
	envelope := func(count int) []byte {
		raw, err := json.Marshal(struct {
			Limit   int             `json:"limit"`
			Query   string          `json:"query"`
			Records []ContextRecord `json:"records"`
		}{8, query, ForContext(records[:count])})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, count := range []int{1, 2, 3} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%d/%d", count, delta), func(t *testing.T) {
				limit := len(envelope(count)) + delta
				want := count
				if delta < 0 {
					want--
				}
				called := false
				client := modelFunc(func(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
					called = true
					if raw := request.Messages[1].Text(); len(raw) > limit || raw != string(envelope(want)) {
						t.Fatalf("incorrect disclosure: limit=%d raw=%s", limit, raw)
					}
					return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"keys":[]}`), FinishReason: model.FinishStop}, nil
				})
				_, err := (ModelRanker{Client: client, Model: "ranker", MaxInputBytes: limit}).Rank(t.Context(), query, records)
				if (err != nil) != (want == 0) || called != (want != 0) {
					t.Fatalf("called=%v want=%d err=%v", called, want, err)
				}
			})
		}
	}
}

func TestModelRankerRejectsUnserializableInputBeforeDispatch(t *testing.T) {
	called := false
	client := modelFunc(func(context.Context, model.Request, model.EventSink) (model.Response, error) {
		called = true
		return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"keys":[]}`), FinishReason: model.FinishStop}, nil
	})
	records := []Record{{Key: "invalid", UpdatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if _, err := (ModelRanker{Client: client, Model: "ranker"}).Rank(t.Context(), "query", records); err == nil || called {
		t.Fatalf("invalid JSON input reached model: called=%v err=%v", called, err)
	}
}

func BenchmarkModelRankerInputPacking(b *testing.B) {
	client := modelFunc(func(context.Context, model.Request, model.EventSink) (model.Response, error) {
		return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"keys":[]}`), FinishReason: model.FinishStop}, nil
	})
	for _, count := range []int{32, MaxPageSize} {
		for _, size := range []int{512, 4096} {
			b.Run(fmt.Sprintf("records=%d/bytes=%d", count, size), func(b *testing.B) {
				records := make([]Record, count)
				for i := range records {
					records[i] = Record{Key: fmt.Sprint(i), Kind: "fact", Content: strings.Repeat("x", size)}
				}
				ranker := ModelRanker{Client: client, Model: "ranker"}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := ranker.Rank(b.Context(), "query", records); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
