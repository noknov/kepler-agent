package runtime

import (
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// Construct the complete projection input before admission. Ephemeral images
// remain absent from durable history but are still charged to this request.
func (r *Runtime) projectionRequest(events []transcript.Event, turn TurnRequest, system model.Message, tools []model.ToolDefinition) ProjectRequest {
	r.eventMu.RLock()
	var sampled uint64
	if state := r.eventCache[turn.SessionID]; state != nil {
		sampled = state.sampledThrough
	}
	r.eventMu.RUnlock()
	return ProjectRequest{TurnID: turn.TurnID, SampledThrough: sampled, Events: events, System: system, Tools: tools, Environment: r.deps.Environment.Message(), CurrentInput: turn.Input}
}
