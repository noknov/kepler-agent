// Package failure defines safe, transport-neutral messages for terminal agent
// failures. Raw errors are diagnostic data and must not cross this boundary.
package failure

import (
	"context"
	"errors"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

const (
	ServiceUnavailableMessage = "The service is temporarily unavailable."
	RequestTimedOutMessage    = "The request timed out before it could finish. Try again with a smaller scope."
	MalformedModelMessage     = "The model returned a malformed tool call and Kepler could not recover after retrying. Please try again."
)

// PublicMessage exposes only a small allowlist of safe failure categories.
// Provider and tool errors can contain arbitrary upstream bodies, schemas,
// request data, or secrets, so their detail never crosses this boundary.
func PublicMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return RequestTimedOutMessage
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if typed, ok := current.(*model.Error); ok && typed.Kind == model.ErrorProtocol {
			return MalformedModelMessage
		}
	}
	return ServiceUnavailableMessage
}
