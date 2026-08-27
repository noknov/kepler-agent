package runtime

import (
	"context"
	"encoding/json"

	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

// WaitingForInput reports whether the latest completed turn paused for input
// from userID. Surfaces use this canonical transcript state to decide whether
// an unaddressed thread reply belongs to the agent.
func (r *Runtime) WaitingForInput(ctx context.Context, sessionID, userID string) (bool, error) {
	events, err := r.deps.Transcript.Load(ctx, sessionID, 0)
	if err != nil {
		return false, err
	}
	turnID := ""
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != transcript.TurnCompleted && event.Type != transcript.TurnFailed && event.Type != transcript.TurnCanceled {
			continue
		}
		if event.Type != transcript.TurnCompleted || event.Status != string(TerminationPendingInput) {
			return false, nil
		}
		turnID = event.TurnID
		break
	}
	if turnID == "" {
		return false, nil
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != transcript.TurnStarted || event.TurnID != turnID {
			continue
		}
		var metadata struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
			return false, nil
		}
		return metadata.UserID != "" && metadata.UserID == userID, nil
	}
	return false, nil
}
