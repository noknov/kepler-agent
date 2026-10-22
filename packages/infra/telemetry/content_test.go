package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestContentCaptureIsOptInAndSanitizesStructuredAndTextSecrets(t *testing.T) {
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "false")
	if ContentRecorder() != nil {
		t.Fatal("content enabled without opt-in")
	}
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "true")
	t.Setenv("EXAMPLE_API_KEY", "opaque-operator-credential")
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	_, span := provider.Tracer("test").Start(context.Background(), "content")
	ContentRecorder()(span, "input", map[string]any{
		"question":     "为什么斜体？ opaque-operator-credential sk-lf-private-test xoxb-private-test Authorization: Bearer private-token",
		"args":         json.RawMessage(`{"nested":{"password":"hidden-password","access_token":"hidden-token"},"query":"SELECT 1"}`),
		"binary":       []byte("private-image-data"),
		"tool_text":    `{"api_key":"quoted-json-secret","refresh_token":"quoted-refresh-secret"} github_pat_example_token_value redis://user:redis-secret@example.invalid`,
		"large_number": json.RawMessage(`{"id":9007199254740993}`),
	})
	span.End()
	var encoded string
	for _, attr := range recorder.Ended()[0].Attributes() {
		if attr.Key == "langfuse.observation.input" {
			encoded = attr.Value.AsString()
		}
	}
	if !json.Valid([]byte(encoded)) || !strings.Contains(encoded, "为什么斜体") || !strings.Contains(encoded, "SELECT 1") {
		t.Fatalf("content=%s", encoded)
	}
	if !strings.Contains(encoded, "9007199254740993") {
		t.Fatal("trace corrupted an exact identifier")
	}
	for _, secret := range []string{"opaque-operator-credential", "private-test", "private-token", "hidden-password", "hidden-token", "private-image-data", "quoted-json-secret", "quoted-refresh-secret", "example_token_value", "redis-secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
}

func TestContentCaptureBoundsEscapedUnicodeAndDeepPayloads(t *testing.T) {
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "true")
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CONTENT_MAX_BYTES", "512")
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	_, span := provider.Tracer("test").Start(context.Background(), "content")
	ContentRecorder()(span, "output", strings.Repeat("中文\n\"", 100000))
	span.End()
	attrs := map[attribute.Key]attribute.Value{}
	for _, attr := range recorder.Ended()[0].Attributes() {
		attrs[attr.Key] = attr.Value
	}
	encoded := attrs["langfuse.observation.output"].AsString()
	if len(encoded) > 512 || !utf8.ValidString(encoded) || !json.Valid([]byte(encoded)) || !attrs["langfuse.observation.metadata.output_truncated"].AsBool() {
		t.Fatalf("invalid bounded capture: %d bytes", len(encoded))
	}
	// A self-referential value cannot make capture recurse indefinitely.
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	_, next := provider.Tracer("test").Start(context.Background(), "cycle")
	ContentRecorder()(next, "input", cycle)
	next.End()
}
