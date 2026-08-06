// Package agentprotocol defines the versioned, transport-neutral contract
// between the agent core and delivery adapters.
package agentprotocol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const Version = "1.0"

type EventType string

const (
	ThreadStarted EventType = "thread.started"
	ThreadClosed  EventType = "thread.closed"
	TurnStarted   EventType = "turn.started"
	TurnCompleted EventType = "turn.completed"
	TurnFailed    EventType = "turn.failed"
	TurnCanceled  EventType = "turn.canceled"
	ItemStarted   EventType = "item.started"
	ItemDelta     EventType = "item.delta"
	ItemCompleted EventType = "item.completed"
	ItemFailed    EventType = "item.failed"
	ItemCanceled  EventType = "item.canceled"
	ErrorOccurred EventType = "error"
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
	StatusInterrupted Status = "interrupted"
	StatusPendingUser Status = "pending_user"
)

type Item struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name,omitempty"`
	Status     Status         `json:"status"`
	Content    string         `json:"content,omitempty"`
	Delta      string         `json:"delta,omitempty"`
	StartedAt  time.Time      `json:"started_at,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Error      string         `json:"error,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type ProtocolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// Event is an extensible envelope. Consumers must ignore unknown fields and
// event types with the same major protocol version.
type Event struct {
	Version  string         `json:"version"`
	ID       string         `json:"id"`
	Sequence uint64         `json:"sequence,omitempty"`
	Type     EventType      `json:"type"`
	ThreadID string         `json:"thread_id"`
	TurnID   string         `json:"turn_id,omitempty"`
	Status   Status         `json:"status,omitempty"`
	Item     *Item          `json:"item,omitempty"`
	Error    *ProtocolError `json:"error,omitempty"`
	At       time.Time      `json:"at"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (e Event) Validate() error {
	version := e.Version
	if version == "" {
		version = Version
	}
	if strings.SplitN(version, ".", 2)[0] != strings.SplitN(Version, ".", 2)[0] {
		return fmt.Errorf("unsupported protocol version %q", version)
	}
	if e.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if e.ThreadID == "" {
		return fmt.Errorf("thread_id is required")
	}
	switch e.Type {
	case TurnStarted, TurnCompleted, TurnFailed, TurnCanceled:
		if e.TurnID == "" {
			return fmt.Errorf("turn_id is required for %s", e.Type)
		}
	case ItemStarted, ItemDelta, ItemCompleted, ItemFailed, ItemCanceled:
		if e.TurnID == "" {
			return fmt.Errorf("turn_id is required for %s", e.Type)
		}
		if e.Item == nil || e.Item.ID == "" || e.Item.Kind == "" {
			return fmt.Errorf("item id and kind are required for %s", e.Type)
		}
	case ErrorOccurred:
		if e.Error == nil || e.Error.Code == "" || e.Error.Message == "" {
			return fmt.Errorf("error code and message are required for %s", e.Type)
		}
	}
	return nil
}

func Normalize(event Event) Event {
	if event.Version == "" {
		event.Version = Version
	}
	if event.ID == "" {
		event.ID = newID("evt")
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	return event
}

func NewTurnID() string { return newID("turn") }

func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(b[:])
	}
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

type Sink interface {
	Publish(context.Context, Event)
}

// EventStore persists lifecycle events and supports replay by per-thread
// sequence. It intentionally extends Sink so durable stores can be wired into
// Core without changing transport adapters.
type EventStore interface {
	Sink
	Append(context.Context, Event) (Event, error)
	Replay(context.Context, string, uint64, int) ([]Event, error)
}

type SinkFunc func(context.Context, Event)

func (f SinkFunc) Publish(ctx context.Context, event Event) {
	if f != nil {
		f(ctx, event)
	}
}

type MultiSink []Sink

func (s MultiSink) Publish(ctx context.Context, event Event) {
	for _, sink := range s {
		if sink != nil {
			sink.Publish(ctx, event)
		}
	}
}
