package telemetry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestSetupWithoutEndpointIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	shutdown, err := Setup(context.Background(), "test")
	if err != nil || shutdown == nil || shutdown(context.Background()) != nil {
		t.Fatalf("shutdown=%v err=%v", shutdown, err)
	}
}

func TestSetupUsesLangfuseCredentialsWhenOTLPIsUnset(t *testing.T) {
	request := make(chan *http.Request, 1)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request <- r
		w.WriteHeader(http.StatusOK)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("LANGFUSE_BASE_URL", server.URL)
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-test")
	shutdown, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("langfuse-test").Start(context.Background(), "test-span")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-request:
		if got, want := received.URL.Path, "/api/public/otel/v1/traces"; got != want {
			t.Fatalf("path=%q want %q", got, want)
		}
		if got, want := received.Header.Get("Authorization"), "Basic cGstdGVzdDpzay10ZXN0"; got != want {
			t.Fatalf("authorization=%q want %q", got, want)
		}
		if got := received.Header.Get("x-langfuse-ingestion-version"); got != "4" {
			t.Fatalf("ingestion version=%q", got)
		}
	default:
		t.Fatal("Langfuse exporter did not send a trace")
	}
}
