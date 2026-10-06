package tool

import (
	"context"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

// ActivationRequest is a proposed model-visible tool set and its acknowledgement.
// The runtime can project both before the catalog publishes any change.
type ActivationRequest struct {
	Definitions []model.ToolDefinition
	Call        Call
	Result      Result
}

type activationAdmissionKey struct{}

// WithActivationAdmission supplies turn-scoped context admission. The callback
// runs under the catalog lock, must be read-only, and must not call the catalog.
func WithActivationAdmission(ctx context.Context, admit func(context.Context, ActivationRequest) error) context.Context {
	return context.WithValue(ctx, activationAdmissionKey{}, admit)
}

func (c *Catalog) activateForCall(ctx context.Context, call Call, names []string, result Result) error {
	var check func([]model.ToolDefinition) error
	if admit, ok := ctx.Value(activationAdmissionKey{}).(func(context.Context, ActivationRequest) error); ok {
		check = func(definitions []model.ToolDefinition) error {
			return admit(ctx, ActivationRequest{Definitions: definitions, Call: call, Result: result})
		}
	}
	return c.activate(call.Scope.SessionID, names, check)
}
