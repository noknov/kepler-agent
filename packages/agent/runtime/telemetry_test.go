package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRuntimeEmitsAgentModelAndToolSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousTracer := runtimeTracer
	runtimeTracer = provider.Tracer("runtime-test")
	t.Cleanup(func() {
		runtimeTracer = previousTracer
		_ = provider.Shutdown(context.Background())
	})

	client := &scriptedModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 4, OutputTokens: 1}},
	}}
	catalog, err := tool.NewCatalog(echoTool{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(Config{Model: "trace-model"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "trace-session", TurnID: "trace-turn", Input: model.TextMessage(model.RoleUser, "run"), Scope: tool.Scope{SessionID: "trace-session", TurnID: "trace-turn", UserID: "user-1", Values: map[string]string{"surface": "web"}}}); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for _, span := range recorder.Ended() {
		counts[span.Name()]++
	}
	if counts["agent.turn"] != 1 || counts["model.generate"] != 2 || counts["tool.execute"] != 1 {
		t.Fatalf("span counts=%v", counts)
	}
	for _, span := range recorder.Ended() {
		attributes := map[attribute.Key]attribute.Value{}
		for _, item := range span.Attributes() {
			attributes[item.Key] = item.Value
		}
		if got := attributes["langfuse.session.id"].AsString(); got != "trace-session" {
			t.Fatalf("%s session=%q", span.Name(), got)
		}
		if got := attributes["langfuse.user.id"].AsString(); got != "user-1" {
			t.Fatalf("%s user=%q", span.Name(), got)
		}
		if got := attributes["langfuse.trace.metadata.surface"].AsString(); got != "web" {
			t.Fatalf("%s surface=%q", span.Name(), got)
		}
	}
}

func TestFallbackTraceUsesActualRouteAndReportsRecovery(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := runtimeTracer
	runtimeTracer = provider.Tracer("test")
	t.Cleanup(func() { runtimeTracer = previous; _ = provider.Shutdown(context.Background()) })
	primary := &scriptedModel{errors: []error{&model.Error{Kind: model.ErrorUnavailable, Retryable: true, Message: "unavailable"}}}
	fallback := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "recovered"), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true}}}}
	client := &model.ResilientClient{Primary: primary, Fallback: fallback, PrimaryProvider: "primary", FallbackProvider: "backup", FallbackModel: "backup-model", MaxAttempts: 1, MinAttemptBudget: time.Millisecond}
	catalog, _ := tool.NewCatalog()
	runner, _ := New(Config{Model: "original-model"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	_, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "fallback", TurnID: "fallback", Input: model.TextMessage(model.RoleUser, "test")})
	if err != nil {
		t.Fatal(err)
	}
	for _, span := range recorder.Ended() {
		attrs := map[attribute.Key]attribute.Value{}
		for _, a := range span.Attributes() {
			attrs[a.Key] = a.Value
		}
		if !attrs["langfuse.observation.metadata.recovered"].AsBool() {
			t.Fatalf("%s did not report recovery", span.Name())
		}
		if span.Name() == "model.generate" {
			if attrs["langfuse.observation.model.name"].AsString() != "backup-model" || attrs["langfuse.observation.metadata.actual_provider"].AsString() != "backup" || attrs["langfuse.observation.metadata.attempt_count"].AsInt64() != 2 {
				t.Fatalf("wrong route: %v", attrs)
			}
		}
	}
}

func TestTraceUsageBucketsDoNotDoubleCountCacheOrReasoning(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		_, span := provider.Tracer("test").Start(context.Background(), "usage")
		recordUsage(span, model.Usage{Reported: true, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 60, CacheCreatedTokens: 10, CacheTokensIncludedInInput: inclusive, ReasoningTokens: 5})
		span.End()
		var usage map[string]int64
		for _, a := range recorder.Ended()[0].Attributes() {
			if a.Key == "langfuse.observation.usage_details" {
				if err := json.Unmarshal([]byte(a.Value.AsString()), &usage); err != nil {
					t.Fatal(err)
				}
			}
		}
		var total int64
		for _, count := range usage {
			total += count
		}
		want := int64(190)
		if inclusive {
			want = 120
		}
		if total != want || usage["output"] != 15 {
			t.Fatalf("usage=%v total=%d want=%d", usage, total, want)
		}
		_ = provider.Shutdown(context.Background())
	}
}

func TestTraceContentOmitsImagesAndInternalReasoningWithoutChangingMessages(t *testing.T) {
	original := model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentText, Text: "answer"}, {Type: model.ContentImage, ImageURL: "data:private"}, {Type: model.ContentReasoning, Text: "internal reasoning"}}}
	encoded, _ := json.Marshal(traceMessages([]model.Message{original}))
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "internal reasoning") || !strings.Contains(string(encoded), "answer") {
		t.Fatal(string(encoded))
	}
	if original.Content[1].ImageURL != "data:private" || len(original.Content) != 3 {
		t.Fatal("instrumentation mutated canonical data")
	}
}

func TestTraceErrorsExcludeProviderBodies(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := runtimeTracer
	runtimeTracer = provider.Tracer("test")
	t.Cleanup(func() { runtimeTracer = previous; _ = provider.Shutdown(context.Background()) })
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	runner, err := New(Config{Model: "test"}, Dependencies{Model: &scriptedModel{errors: []error{errors.New("private-provider-body-sentinel")}}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = runner.RunTurn(context.Background(), TurnRequest{SessionID: "test", TurnID: "test", Input: model.TextMessage(model.RoleUser, "synthetic")})
	if len(recorder.Ended()) != 2 {
		t.Fatal("expected agent and generation spans")
	}
	for _, span := range recorder.Ended() {
		if span.Status().Code != codes.Error {
			t.Fatalf("%s not marked failed", span.Name())
		}
		if strings.Contains(fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "private-provider-body-sentinel") {
			t.Fatal("provider body leaked into trace")
		}
	}
	events, _ := store.Load(context.Background(), "test", 0)
	encoded, _ := json.Marshal(events)
	if !strings.Contains(string(encoded), "private-provider-body-sentinel") {
		t.Fatal("local diagnostic was lost")
	}
}

func TestToolErrorResultsAndPanicsAreFailedSpans(t *testing.T) {
	for _, item := range []tool.Tool{emptyResultTool{}, panicTool{}} {
		t.Run(item.Descriptor().Name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := runtimeTracer
			runtimeTracer = provider.Tracer("test")
			t.Cleanup(func() { runtimeTracer = previous; _ = provider.Shutdown(context.Background()) })
			catalog, _ := tool.NewCatalog(item)
			client := &scriptedModel{responses: []model.Response{
				{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "test", Name: item.Descriptor().Name, Arguments: json.RawMessage(`{}`)}}}}, FinishReason: model.FinishToolCalls},
				{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop},
			}}
			runner, err := New(Config{Model: "test"}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.RunTurn(context.Background(), TurnRequest{SessionID: "test", TurnID: "test", Input: model.TextMessage(model.RoleUser, "synthetic")})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, span := range recorder.Ended() {
				if span.Name() == "tool.execute" {
					found = true
					if span.Status().Code != codes.Error {
						t.Fatal("error result reported healthy")
					}
				}
			}
			if !found {
				t.Fatal("tool span missing")
			}
		})
	}
}

func TestRuntimePersistsTurnTraceIdentity(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousTracer := runtimeTracer
	runtimeTracer = provider.Tracer("runtime-test")
	t.Cleanup(func() {
		runtimeTracer = previousTracer
		_ = provider.Shutdown(context.Background())
	})

	store := transcript.NewMemoryStore()
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(Config{Model: "trace-model"}, Dependencies{Model: &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "trace-session", TurnID: "trace-turn", Input: model.TextMessage(model.RoleUser, "run")}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(context.Background(), "trace-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	var modelTrace *transcript.TraceContext
	for _, event := range events {
		if event.Type == transcript.TurnStarted {
			if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
		}
		if event.Type == transcript.ModelCompleted {
			modelTrace = event.Trace
		}
	}
	traceID, _ := metadata["trace_id"].(string)
	spanID, _ := metadata["span_id"].(string)
	if _, err := trace.TraceIDFromHex(traceID); err != nil || traceID == "" {
		t.Fatalf("trace_id=%q err=%v", traceID, err)
	}
	if _, err := trace.SpanIDFromHex(spanID); err != nil || spanID == "" {
		t.Fatalf("span_id=%q err=%v", spanID, err)
	}
	if modelTrace == nil || modelTrace.SpanID == spanID || modelTrace.ParentSpanID != spanID {
		t.Fatalf("model trace=%+v root span=%q", modelTrace, spanID)
	}
}

func TestResilientAttemptsDoNotDuplicateLogicalModelCompletion(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	primary := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}
	client := &model.ResilientClient{Primary: primary, PrimaryProvider: "test", MaxAttempts: 1}
	runner, err := New(Config{Model: "trace-model"}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "attempt-session", TurnID: "attempt-turn", Input: model.TextMessage(model.RoleUser, "run")}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(context.Background(), "attempt-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	completed, attempts := 0, 0
	for _, event := range events {
		switch event.Type {
		case transcript.ModelCompleted:
			completed++
		case transcript.ModelAttempted:
			attempts++
		}
	}
	if completed != 1 || attempts != 2 {
		t.Fatalf("completed=%d attempts=%d, want one logical completion and requested/completed attempt facts", completed, attempts)
	}
}
