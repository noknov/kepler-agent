package telemetry

import (
	"context"
	"testing"
)

func TestSetupWithoutEndpointIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	shutdown, err := Setup(context.Background(), "test")
	if err != nil || shutdown == nil || shutdown(context.Background()) != nil {
		t.Fatalf("shutdown=%v err=%v", shutdown, err)
	}
}
