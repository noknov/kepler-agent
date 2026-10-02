package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type failingExporter struct{ tracetest.InMemoryExporter }

func (*failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("private provider body")
}

func TestExporterStatusSeparatesDeliveryAndFailure(t *testing.T) {
	state := &exportState{status: Status{Configured: true, Backend: "langfuse"}}
	exporter := &trackedExporter{delegate: tracetest.NewInMemoryExporter(), state: state}
	if err := exporter.ExportSpans(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	exporter.delegate = &failingExporter{}
	if err := exporter.ExportSpans(context.Background(), nil); err == nil {
		t.Fatal("export failure hidden")
	}
	if state.status.ExportedBatches != 1 || state.status.FailedBatches != 1 || state.status.LastSuccessAt == nil || state.status.LastFailureAt == nil {
		t.Fatal(state.status)
	}
}

func TestDeploymentAttributesReachEverySpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSpanProcessor(deploymentAttributes{attributes: []attribute.KeyValue{attribute.String("langfuse.environment", "test"), attribute.String("langfuse.release", "revision")}}))
	defer provider.Shutdown(context.Background())
	ctx, root := provider.Tracer("test").Start(context.Background(), "root")
	_, child := provider.Tracer("test").Start(ctx, "child")
	child.End()
	root.End()
	for _, span := range recorder.Ended() {
		attrs := map[string]string{}
		for _, a := range span.Attributes() {
			attrs[string(a.Key)] = a.Value.AsString()
		}
		if attrs["langfuse.environment"] != "test" || attrs["langfuse.release"] != "revision" {
			t.Fatalf("missing deployment attributes: %s", span.Name())
		}
	}
}

func TestPartialCredentialsAndUnsafeBaseURLFailWithoutLeaking(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "")
	if _, err := Setup(context.Background(), "test"); err == nil {
		t.Fatal("partial config silently disabled")
	}
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-private")
	t.Setenv("LANGFUSE_BASE_URL", "https://user:private@example.com")
	if _, err := Setup(context.Background(), "test"); err == nil || err.Error() != "LANGFUSE_BASE_URL must be an HTTP(S) base URL without credentials, query or fragment" {
		t.Fatalf("invalid URL diagnostic: %v", err)
	}
}
