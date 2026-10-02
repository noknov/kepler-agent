package telemetry

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Status distinguishes configuration from successful OTLP batch delivery. It
// never contains endpoints, keys, headers, or an upstream error body.
type Status struct {
	Configured      bool       `json:"configured"`
	Backend         string     `json:"backend"`
	ServiceName     string     `json:"service_name"`
	Environment     string     `json:"environment,omitempty"`
	Release         string     `json:"release,omitempty"`
	ExportedBatches int64      `json:"exported_batches"`
	FailedBatches   int64      `json:"failed_batches"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt   *time.Time `json:"last_failure_at,omitempty"`
}

type exportState struct {
	mu     sync.Mutex
	status Status
}

var currentMu sync.RWMutex
var current = &exportState{status: Status{Backend: "disabled"}}

func CurrentStatus() Status {
	currentMu.RLock()
	state := current
	currentMu.RUnlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.status
}
func publishStatus(state *exportState) { currentMu.Lock(); current = state; currentMu.Unlock() }

type trackedExporter struct {
	delegate sdktrace.SpanExporter
	state    *exportState
}

func (e *trackedExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.delegate.ExportSpans(ctx, spans)
	now := time.Now().UTC()
	e.state.mu.Lock()
	if err == nil {
		e.state.status.ExportedBatches++
		e.state.status.LastSuccessAt = &now
	} else {
		e.state.status.FailedBatches++
		e.state.status.LastFailureAt = &now
	}
	e.state.mu.Unlock()
	return err
}
func (e *trackedExporter) Shutdown(ctx context.Context) error { return e.delegate.Shutdown(ctx) }

type deploymentAttributes struct{ attributes []attribute.KeyValue }

func (p deploymentAttributes) OnStart(_ context.Context, span sdktrace.ReadWriteSpan) {
	span.SetAttributes(p.attributes...)
}
func (deploymentAttributes) OnEnd(sdktrace.ReadOnlySpan)      {}
func (deploymentAttributes) Shutdown(context.Context) error   { return nil }
func (deploymentAttributes) ForceFlush(context.Context) error { return nil }
