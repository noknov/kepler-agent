// Package telemetry configures standard OpenTelemetry export without coupling
// the agent runtime to a vendor. When no OTLP endpoint is configured, the
// global no-op provider remains in place.
package telemetry

import (
	"context"
	"encoding/base64"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Shutdown func(context.Context) error

func Setup(ctx context.Context, fallbackServiceName string) (Shutdown, error) {
	options, configured := langfuseOptions()
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" || strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != "" {
		// A standard OTLP endpoint is authoritative. This keeps the runtime
		// vendor-neutral and lets operators fan out through an OTel Collector.
		options = nil
		configured = true
	}
	if !configured {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = fallbackServiceName
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}

// langfuseOptions makes Langfuse a convenient opt-in OTLP backend while
// preserving standard OTLP configuration as the preferred integration point.
// It intentionally records no content itself; callers decide span attributes.
func langfuseOptions() ([]otlptracehttp.Option, bool) {
	publicKey := strings.TrimSpace(os.Getenv("LANGFUSE_PUBLIC_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("LANGFUSE_SECRET_KEY"))
	if publicKey == "" || secretKey == "" {
		return nil, false
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("LANGFUSE_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://cloud.langfuse.com"
	}
	authorization := base64.StdEncoding.EncodeToString([]byte(publicKey + ":" + secretKey))
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(baseURL + "/api/public/otel/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization":                "Basic " + authorization,
			"x-langfuse-ingestion-version": "4",
		}),
	}, true
}
