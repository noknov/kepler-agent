package web

import (
	"context"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/safety"
)

type ClientEvent struct {
	ID         string    `json:"id"`
	Sequence   uint64    `json:"sequence,omitempty"`
	SessionID  string    `json:"conversationId"`
	TurnID     string    `json:"turnId,omitempty"`
	Kind       string    `json:"kind"`
	Role       string    `json:"role,omitempty"`
	Text       string    `json:"text,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Status     string    `json:"status,omitempty"`
	ToolCallID string    `json:"toolCallId,omitempty"`
	At         time.Time `json:"at,omitempty"`
}

type EventHub struct {
	Redactor safety.Redactor

	mu          sync.Mutex
	subscribers map[string]map[chan ClientEvent]struct{}
	streams     map[string]*safety.StreamRedactor
}

func NewEventHub(redactor safety.Redactor) *EventHub {
	return &EventHub{Redactor: redactor, subscribers: make(map[string]map[chan ClientEvent]struct{}), streams: make(map[string]*safety.StreamRedactor)}
}

func (h *EventHub) Subscribe(sessionID string) (<-chan ClientEvent, func()) {
	channel := make(chan ClientEvent, 128)
	h.mu.Lock()
	if h.subscribers[sessionID] == nil {
		h.subscribers[sessionID] = make(map[chan ClientEvent]struct{})
	}
	h.subscribers[sessionID][channel] = struct{}{}
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if _, exists := h.subscribers[sessionID][channel]; exists {
			delete(h.subscribers[sessionID], channel)
			close(channel)
		}
		if len(h.subscribers[sessionID]) == 0 {
			delete(h.subscribers, sessionID)
		}
		h.mu.Unlock()
	}
}

func (h *EventHub) Publish(_ context.Context, event transcript.Event) {
	if h == nil || !isWebSession(event.SessionID) {
		return
	}
	if event.Type == transcript.ModelStreamed && event.Model != nil && event.Model.Type == model.StreamTextDelta {
		h.mu.Lock()
		redactor := h.streams[event.TurnID]
		if redactor == nil {
			redactor = safety.NewStreamRedactor(h.Redactor)
			h.streams[event.TurnID] = redactor
		}
		text := redactor.Append(event.Model.Text)
		h.mu.Unlock()
		if text != "" {
			h.broadcast(ClientEvent{ID: event.ID, SessionID: event.SessionID, TurnID: event.TurnID, Kind: "assistant_delta", Role: "assistant", Text: text, At: event.Timestamp})
		}
		return
	}
	if event.Type == transcript.AssistantMessage || event.Type == transcript.TurnCompleted || event.Type == transcript.TurnFailed || event.Type == transcript.TurnCanceled {
		h.mu.Lock()
		if redactor := h.streams[event.TurnID]; redactor != nil {
			if tail := redactor.Flush(); tail != "" {
				h.broadcastLocked(ClientEvent{ID: event.ID + ":tail", SessionID: event.SessionID, TurnID: event.TurnID, Kind: "assistant_delta", Role: "assistant", Text: tail, At: event.Timestamp})
			}
			delete(h.streams, event.TurnID)
		}
		h.mu.Unlock()
	}
	if view, ok := ProjectEvent(event, h.Redactor); ok {
		h.broadcast(view)
	}
}

func ProjectEvent(event transcript.Event, redactor safety.Redactor) (ClientEvent, bool) {
	view := ClientEvent{ID: event.ID, Sequence: event.Sequence, SessionID: event.SessionID, TurnID: event.TurnID, At: event.Timestamp, Status: event.Status}
	switch event.Type {
	case transcript.UserInput:
		if event.Message == nil || isHiddenInput(*event.Message) {
			return ClientEvent{}, false
		}
		view.Kind, view.Role, view.Text = "message", "user", redactor.Sanitize(event.Message.Text())
	case transcript.AssistantMessage:
		if event.Message == nil || len(event.Message.ToolCalls()) > 0 {
			return ClientEvent{}, false
		}
		view.Kind, view.Role, view.Text = "message", "assistant", redactor.Sanitize(event.Message.Text())
	case transcript.ToolCallStarted:
		if event.ToolCall == nil {
			return ClientEvent{}, false
		}
		view.Kind, view.Tool, view.ToolCallID, view.Status = "tool", event.ToolCall.Name, event.ToolCall.ID, "running"
	case transcript.ToolCallCompleted:
		if event.ToolCall == nil {
			return ClientEvent{}, false
		}
		view.Kind, view.Tool, view.ToolCallID, view.Status = "tool", event.ToolCall.Name, event.ToolCall.ID, "completed"
	case transcript.ToolCallFailed:
		if event.ToolCall == nil {
			return ClientEvent{}, false
		}
		view.Kind, view.Tool, view.ToolCallID, view.Status = "tool", event.ToolCall.Name, event.ToolCall.ID, "failed"
	case transcript.ApprovalRequested:
		if event.ToolCall == nil {
			return ClientEvent{}, false
		}
		view.Kind, view.Tool, view.ToolCallID, view.Status = "approval", event.ToolCall.Name, event.ToolCall.ID, "pending"
	case transcript.ApprovalResolved:
		if event.ToolCall == nil {
			return ClientEvent{}, false
		}
		view.Kind, view.Tool, view.ToolCallID, view.Status = "approval", event.ToolCall.Name, event.ToolCall.ID, "resolved"
	case transcript.TurnStarted:
		view.Kind, view.Status = "turn", "running"
	case transcript.TurnCompleted:
		view.Kind = "turn"
		if view.Status == "" {
			view.Status = "completed"
		}
	case transcript.TurnFailed:
		view.Kind, view.Status, view.Text = "turn", "failed", "The assistant could not complete this turn."
	case transcript.TurnCanceled:
		view.Kind, view.Status = "turn", "canceled"
	default:
		return ClientEvent{}, false
	}
	return view, true
}

func (h *EventHub) broadcast(event ClientEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcastLocked(event)
}

func (h *EventHub) broadcastLocked(event ClientEvent) {
	for channel := range h.subscribers[event.SessionID] {
		select {
		case channel <- event:
		default:
			delete(h.subscribers[event.SessionID], channel)
			close(channel)
		}
	}
}

func isHiddenInput(message model.Message) bool {
	return len(message.ID) > len("approval-continuation:") && message.ID[:len("approval-continuation:")] == "approval-continuation:"
}

func isWebSession(sessionID string) bool {
	return len(sessionID) > 4 && sessionID[:4] == "web_"
}
