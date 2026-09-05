package runtime

import (
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"go.opentelemetry.io/otel/attribute"
)

// langfuseTraceAttributes are copied onto every runtime span. Langfuse v4
// queries observations directly, so putting user/session/surface only on the
// root span would make child model and tool spans difficult to filter.
// Content is deliberately excluded: the canonical transcript is the sole
// store for prompts, tool arguments, and model output.
func langfuseTraceAttributes(scope tool.Scope) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String("langfuse.session.id", scope.SessionID),
		attribute.String("langfuse.trace.name", "kepler-agent.turn"),
	}
	if scope.UserID != "" {
		attributes = append(attributes, attribute.String("langfuse.user.id", scope.UserID))
	}
	if surface := strings.TrimSpace(scope.Values["surface"]); surface != "" {
		attributes = append(attributes,
			attribute.String("agent.surface", surface),
			attribute.String("langfuse.trace.metadata.surface", surface),
			attribute.String("langfuse.observation.metadata.surface", surface),
		)
	}
	return attributes
}

func langfuseObservationAttributes(scope tool.Scope, observationType string) []attribute.KeyValue {
	attributes := langfuseTraceAttributes(scope)
	return append(attributes, attribute.String("langfuse.observation.type", observationType))
}
