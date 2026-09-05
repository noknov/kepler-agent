// Package trajectory derives a safe, read-only operational view from canonical
// transcript facts. It never becomes another conversation state store.
package trajectory

import (
	"encoding/json"
	"sort"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type Item struct {
	Sequence uint64                   `json:"sequence"`
	Type     transcript.EventType     `json:"type"`
	TurnID   string                   `json:"turn_id,omitempty"`
	Status   string                   `json:"status,omitempty"`
	At       string                   `json:"at"`
	Trace    *transcript.TraceContext `json:"trace,omitempty"`
	Summary  map[string]any           `json:"summary,omitempty"`
}

// Build returns metadata and structural facts only. User/model/tool content
// and arbitrary tool arguments are deliberately excluded from the default
// operator view.
func Build(events []transcript.Event) []Item {
	items := make([]Item, 0, len(events))
	for _, event := range events {
		summary := map[string]any{}
		if event.ToolCall != nil {
			summary["tool_name"] = event.ToolCall.Name
			summary["tool_call_id"] = event.ToolCall.ID
			summary["args_bytes"] = len(event.ToolCall.Arguments)
		}
		if event.ToolResult != nil {
			summary["tool_error"] = event.ToolResult.IsError
			summary["tool_error_code"] = event.ToolResult.ErrorCode
			summary["tool_truncated"] = event.ToolResult.Truncated
		}
		var metadata map[string]any
		if json.Unmarshal(event.Metadata, &metadata) == nil {
			for _, key := range []string{"request_id", "attempt", "provider", "model", "fallback", "outcome", "kind", "estimated_tokens", "message_count", "prompt_hash", "tool_count", "step"} {
				if value, ok := metadata[key]; ok {
					summary[key] = value
				}
			}
		}
		items = append(items, Item{Sequence: event.Sequence, Type: event.Type, TurnID: event.TurnID, Status: event.Status, At: event.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z"), Trace: event.Trace, Summary: summary})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	return items
}
