// Package transcript contains the append-only canonical session history.
package transcript

import (
	"context"
	"encoding/json"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/tool"
)

type EventType string

const (
	SessionStarted     EventType = "session_started"
	TurnStarted        EventType = "turn_started"
	UserInput          EventType = "user_input"
	SteeringInput      EventType = "steering_input"
	RuntimeInstruction EventType = "runtime_instruction"
	ContextProjected   EventType = "context_projected"
	ModelRequested     EventType = "model_requested"
	ModelStreamed      EventType = "model_streamed"
	AssistantMessage   EventType = "assistant_message"
	ToolCallStarted    EventType = "tool_call_started"
	ToolCallCompleted  EventType = "tool_call_completed"
	ToolCallFailed     EventType = "tool_call_failed"
	ApprovalRequested  EventType = "approval_requested"
	ApprovalResolved   EventType = "approval_resolved"
	CompactionCreated  EventType = "compaction_created"
	TurnCompleted      EventType = "turn_completed"
	TurnFailed         EventType = "turn_failed"
	TurnCanceled       EventType = "turn_canceled"
)

type Event struct {
	ID         string             `json:"id"`
	Sequence   uint64             `json:"sequence"`
	SessionID  string             `json:"session_id"`
	TurnID     string             `json:"turn_id,omitempty"`
	Type       EventType          `json:"type"`
	Timestamp  time.Time          `json:"timestamp"`
	Message    *model.Message     `json:"message,omitempty"`
	Model      *model.StreamEvent `json:"model_event,omitempty"`
	ToolCall   *tool.Call         `json:"tool_call,omitempty"`
	ToolResult *tool.Result       `json:"tool_result,omitempty"`
	Status     string             `json:"status,omitempty"`
	Error      string             `json:"error,omitempty"`
	Metadata   json.RawMessage    `json:"metadata,omitempty"`
}

type Store interface {
	Append(ctx context.Context, event Event) (Event, error)
	Load(ctx context.Context, sessionID string, afterSequence uint64) ([]Event, error)
}

type Sink interface {
	Publish(ctx context.Context, event Event)
}

type SinkFunc func(context.Context, Event)

func (f SinkFunc) Publish(ctx context.Context, event Event) { f(ctx, event) }

type MultiSink []Sink

func (s MultiSink) Publish(ctx context.Context, event Event) {
	for _, sink := range s {
		if sink != nil {
			sink.Publish(ctx, event)
		}
	}
}
