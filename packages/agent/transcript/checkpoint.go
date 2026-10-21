package transcript

import (
	"context"
	"encoding/json"
)

// TurnLoader loads a continuation window plus the requested turn's accounting
// events. Load remains the complete, unmodified audit/replay interface.
type TurnLoader interface {
	LoadTurn(context.Context, string, string) ([]Event, error)
}

// CheckpointCoverage accepts only backward-pointing, message-bearing checkpoints.
func CheckpointCoverage(event Event) uint64 {
	if event.Type != CompactionCreated || event.Message == nil {
		return 0
	}
	var data struct {
		Coverage uint64 `json:"covers_through"`
	}
	if json.Unmarshal(event.Metadata, &data) != nil || data.Coverage == 0 || data.Coverage >= event.Sequence {
		return 0
	}
	return data.Coverage
}

// TurnWindow keeps the latest continuation, the original current input, and all
// facts required to recover or replay the requested turn. No canonical data is deleted.
func TurnWindow(events []Event, turnID string) []Event {
	var coverage, input uint64
	for _, event := range events {
		if value := CheckpointCoverage(event); value > 0 {
			coverage = value
		}
		if event.Type == UserInput || event.Type == SteeringInput {
			input = event.Sequence
		}
	}
	var out []Event
	for _, event := range events {
		if event.Sequence > coverage || (turnID != "" && event.TurnID == turnID) || event.Sequence == input {
			out = append(out, event)
		}
	}
	return out
}
