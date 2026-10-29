package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func recordRecallSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	return recorder
}

type failingRecallStore struct {
	Store
	cause error
}

func (s failingRecallStore) Query(context.Context, Identity, Query) (Page, error) {
	return Page{}, s.cause
}

func TestRecallFailureMarksSpanWithoutLeakingError(t *testing.T) {
	const private = "private-store-error-61294"
	recorder := recordRecallSpans(t)
	cause := &model.Error{Kind: model.ErrorTransient, Message: private}
	_, err := Recall(t.Context(), failingRecallStore{cause: cause}, Identity{TeamID: "team", UserID: "user"}, "query", RecallPolicy{}, nil)
	spans := recorder.Ended()
	if err != cause || len(spans) != 1 || spans[0].Status().Code != codes.Error {
		t.Fatalf("failed recall not observable: err=%v spans=%v", err, spans)
	}
	if data := fmt.Sprintf("%+v %+v %+v", spans[0].Attributes(), spans[0].Events(), spans[0].Status()); strings.Contains(data, private) {
		t.Fatalf("store error leaked into telemetry: %s", data)
	}
}

func TestRecallReportsRankingOutcomeWithoutPrivateContent(t *testing.T) {
	const private = "private-memory-marker-88421"
	for _, test := range []struct {
		name, outcome string
		ranker        Ranker
		empty         bool
		want          int
	}{
		{"disabled", "disabled", nil, false, 1},
		{"empty", "no_candidates", rankFunc(func(context.Context, string, []Record) ([]string, error) {
			return nil, fmt.Errorf("unexpected ranking of empty candidates")
		}), true, 0},
		{"completed", "completed", rankFunc(func(context.Context, string, []Record) ([]string, error) { return []string{"known"}, nil }), false, 1},
		{"abstained", "completed", rankFunc(func(context.Context, string, []Record) ([]string, error) { return []string{}, nil }), false, 0},
		{"generation failure", "ranker_error", rankFunc(func(context.Context, string, []Record) ([]string, error) {
			return nil, &model.Error{Kind: model.ErrorAuth, Message: private}
		}), false, 1},
		{"invalid keys", "invalid_selection", rankFunc(func(context.Context, string, []Record) ([]string, error) { return []string{private}, nil }), false, 1},
		{"invalid model output", "invalid_selection", ModelRanker{Model: "ranker", Client: modelFunc(func(context.Context, model.Request, model.EventSink) (model.Response, error) {
			return model.Response{Message: model.TextMessage(model.RoleAssistant, `{"keys":["`+private+`"]}`), FinishReason: model.FinishStop}, nil
		})}, false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := recordRecallSpans(t)
			records := []Record{{Key: "known", Kind: "fact", Content: private}}
			if test.empty {
				records = nil
			}
			store := &queryStore{query: func(q Query) Page {
				if q.ExcludePreferences || q.Keys != nil {
					return Page{Records: records}
				}
				return Page{}
			}}
			page, err := Recall(t.Context(), store, Identity{TeamID: private, UserID: private}, private, RecallPolicy{}, test.ranker)
			if err != nil || len(page.Records) != test.want {
				t.Fatalf("fallback/disclosure changed: page=%+v err=%v", page, err)
			}
			spans := recorder.Ended()
			if len(spans) != 1 || spans[0].Name() != "memory.recall" {
				t.Fatalf("recall outcome is invisible: spans=%v", spans)
			}
			span := spans[0]
			var outcome string
			for _, attr := range span.Attributes() {
				if attr.Key == "agent.memory.rerank.outcome" {
					outcome = attr.Value.AsString()
				}
			}
			if outcome != test.outcome || span.Status().Code == codes.Error {
				t.Fatalf("successful recall misclassified: outcome=%s status=%+v", outcome, span.Status())
			}
			if data := fmt.Sprintf("%+v %+v %+v", span.Attributes(), span.Events(), span.Status()); strings.Contains(data, private) {
				t.Fatalf("private query, memory, identity or error leaked into telemetry: %s", data)
			}
		})
	}
}
