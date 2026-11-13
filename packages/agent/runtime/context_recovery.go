package runtime

import (
	"context"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

// A provider rejection requests structural history reduction, not a guessed
// smaller model window. Recovery uses the normal projector and checkpoint owner.
func (r *Runtime) forceCompactAfterContextLimit(ctx context.Context, request TurnRequest, system model.Message) error {
	if r.deps.Compactor == nil {
		return fmt.Errorf("compactor unavailable")
	}
	events, err := r.contextEvents(ctx, request.SessionID)
	if err != nil {
		return err
	}
	projectRequest := r.projectionRequest(events, request, system, r.deps.Tools.ActiveDefinitions(request.SessionID))
	projectRequest.CompactHistory = true
	projection, err := r.deps.Projector.Project(ctx, projectRequest)
	if err != nil {
		return err
	}
	if len(projection.Dropped) == 0 {
		return fmt.Errorf("context limit cannot be recovered without dropping the current input or latest tool exchange")
	}
	return r.compactContext(ctx, request, projection, true)
}
