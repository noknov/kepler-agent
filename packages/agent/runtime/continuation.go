package runtime

import (
	"context"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// ConnectionContinuation is a transport-neutral request to resume a session
// after an external credential has been granted.
type ConnectionContinuation struct {
	AuthorizationRevision string
	Provider              string
	InstanceID            string
	TurnID                string
	Scope                 tool.Scope
}

// ConnectionContinuationStore is implemented by product adapters. The shared
// runtime does not know whether a connection comes from Slack, CLI, or another
// surface.
type ConnectionContinuationStore interface {
	Save(context.Context, ConnectionContinuation) error
}

// Persist the dependency before publishing a wait. A storage or binding failure
// becomes an explicit failed result; sibling function calls still get receipts.
func (r *Runtime) prepareContinuation(ctx context.Context, request TurnRequest, call tool.Call, result *tool.Result) (*tool.Result, error) {
	if result.ErrorCode != "connection_required" || r.deps.ConnectionContinuations == nil {
		return result, nil
	}
	provider, _ := result.Metadata["provider"].(string)
	instance, _ := result.Metadata["instance_id"].(string)
	revision, _ := result.Metadata["authorization_revision"].(string)
	var err error
	if provider == "" || call.Scope.UserID == "" {
		err = fmt.Errorf("connection continuation requires a provider and authenticated actor")
	} else {
		err = r.deps.ConnectionContinuations.Save(ctx, ConnectionContinuation{AuthorizationRevision: revision, Provider: provider, InstanceID: instance, TurnID: request.TurnID, Scope: call.Scope})
	}
	if err == nil {
		return result, nil
	}
	failed := *result
	failed.NeedsUserInput = false
	failed.ErrorCode = "connection_handoff_failed"
	failed.Content = tool.TextResult("The required connection could not be registered for automatic continuation. Retry the request after storage recovers.").Content
	return &failed, fmt.Errorf("save connection continuation: %w", err)
}
