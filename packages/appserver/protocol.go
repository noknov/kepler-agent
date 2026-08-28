package appserver

import (
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
)

// ThreadStartParams creates a new conversation session.
type ThreadStartParams struct {
	SessionID string `json:"sessionId,omitempty"`
	UserID    string `json:"userId,omitempty"`
}

// ThreadResumeParams loads an existing session transcript.
type ThreadResumeParams struct {
	SessionID     string `json:"sessionId"`
	AfterSequence uint64 `json:"afterSequence,omitempty"`
	IncludeEvents bool   `json:"includeEvents,omitempty"`
	StreamItems   bool   `json:"streamItems,omitempty"`
}

// ThreadForkParams branches a session at a transcript sequence boundary.
type ThreadForkParams struct {
	SourceSessionID string `json:"sourceSessionId"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	BeforeSequence  uint64 `json:"beforeSequence,omitempty"`
}

// TurnInterruptParams cancels an active turn.
type TurnInterruptParams struct {
	TurnID string `json:"turnId"`
}

// NotificationMethod returns the JSON-RPC notification name for a transcript event.
func NotificationMethod(eventType string, streamed bool) string {
	switch eventType {
	case "tool_call_started":
		return "item/started"
	case "tool_call_completed", "tool_call_failed":
		return "item/completed"
	case "model_streamed":
		if streamed {
			return "item/agentMessage/delta"
		}
		return "item/updated"
	case "assistant_message":
		return "item/completed"
	case "user_input", "steering_input":
		return "item/started"
	case "approval_requested":
		return "item/approvalRequested"
	case "approval_resolved":
		return "item/approvalResolved"
	default:
		return "event"
	}
}

// DefaultCapabilities lists the app-server protocol surface.
func DefaultCapabilities() []string {
	return []string{
		"thread/start",
		"thread/resume",
		"thread/fork",
		"turn/start",
		"turn/steer",
		"turn/interrupt",
		"approval/resolve",
		"event",
		"item/started",
		"item/updated",
		"item/completed",
		"item/agentMessage/delta",
		"item/approvalRequested",
		"item/approvalResolved",
		"turn/started",
		"turn/completed",
	}
}

// NewTurnID allocates a unique turn identifier.
func NewTurnID(ids agentruntime.IDGenerator) string {
	if ids == nil {
		ids = agentruntime.RandomIDs{}
	}
	return ids.New("turn")
}

// NewSessionID allocates a unique session identifier.
func NewSessionID(ids agentruntime.IDGenerator) string {
	if ids == nil {
		ids = agentruntime.RandomIDs{}
	}
	return ids.New("ses")
}
