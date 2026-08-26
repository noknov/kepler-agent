package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "trace-session", TurnID: "trace-turn", Input: model.TextMessage(model.RoleUser, "run")}); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for _, span := range recorder.Ended() {
		counts[span.Name()]++
	}
	if counts["agent.turn"] != 1 || counts["model.generate"] != 2 || counts["tool.execute"] != 1 {
		t.Fatalf("span counts=%v", counts)
	}
}
