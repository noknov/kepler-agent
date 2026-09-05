package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"go.opentelemetry.io/otel/attribute"
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
	for _, event := range events {
		if event.Type == transcript.TurnStarted {
			if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
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
}
