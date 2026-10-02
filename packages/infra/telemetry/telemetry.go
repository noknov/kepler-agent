// Package telemetry configures standard OpenTelemetry export without coupling
// the agent runtime to a vendor. When no OTLP endpoint is configured, the
// global no-op provider remains in place.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"runtime/debug"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

type Shutdown func(context.Context) error

// BuildRevision may be supplied by an artifact build that excludes .git.
var BuildRevision string

func releaseVersion() string {
	if BuildRevision != "" {
		return BuildRevision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision string
		modified := false
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				modified = setting.Value == "true"
			}
		}
		if revision != "" && modified {
			revision += "+modified"
		}
		return revision
	}
	return ""
}

func Setup(ctx context.Context, fallbackServiceName string) (Shutdown, error) {
	standard := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" || strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != ""
	publicKey, secretKey := strings.TrimSpace(os.Getenv("LANGFUSE_PUBLIC_KEY")), strings.TrimSpace(os.Getenv("LANGFUSE_SECRET_KEY"))
	if !standard && ((publicKey == "") != (secretKey == "")) {
		return nil, fmt.Errorf("LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY must both be set")
	}
	if !standard && publicKey != "" {
		base := strings.TrimSpace(os.Getenv("LANGFUSE_BASE_URL"))
		if base != "" {
			parsed, err := url.Parse(base)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, fmt.Errorf("LANGFUSE_BASE_URL must be an HTTP(S) base URL without credentials, query or fragment")
			}
		}
	}
	options, configured := langfuseOptions()
	backend := "langfuse"
	if standard {
		// A standard OTLP endpoint is authoritative. This keeps the runtime
		// vendor-neutral and lets operators fan out through an OTel Collector.
		options = nil
		configured = true
		backend = "otlp"
	}
	if !configured {
		publishStatus(&exportState{status: Status{Backend: "disabled", ServiceName: fallbackServiceName}})
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
	status := Status{Configured: true, Backend: backend, ServiceName: serviceName}
	var deployment []attribute.KeyValue
	for _, item := range res.Attributes() {
		switch string(item.Key) {
		case "deployment.environment.name":
			status.Environment = item.Value.AsString()
			deployment = append(deployment, attribute.String("langfuse.environment", status.Environment))
		case "service.version":
			status.Release = item.Value.AsString()
			deployment = append(deployment, attribute.String("langfuse.release", status.Release))
		}
	}
	if status.Release == "" {
		status.Release = releaseVersion()
		if status.Release != "" {
			deployment = append(deployment, attribute.String("service.version", status.Release), attribute.String("langfuse.release", status.Release))
		}
	}
	state := &exportState{status: status}
	publishStatus(state)
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(&trackedExporter{delegate: exporter, state: state}), sdktrace.WithResource(res), sdktrace.WithSpanProcessor(deploymentAttributes{attributes: deployment}))
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
