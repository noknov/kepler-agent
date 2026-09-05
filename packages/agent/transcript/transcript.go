// Package transcript contains the append-only canonical session history.
package transcript

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type EventType string

const (
	SessionStarted   EventType = "session_started"
	TurnStarted      EventType = "turn_started"
	StepStarted      EventType = "step_started"
	StepCompleted    EventType = "step_completed"
	UserInput        EventType = "user_input"
	SteeringInput    EventType = "steering_input"
	ContextProjected EventType = "context_projected"
	ModelRequested   EventType = "model_requested"
	// ModelRequestStarted records the durable intent to make one logical model
	// request. Unlike ModelRequested (a legacy attempt/projection event), it
	// carries a stable request ID that recovery can reconcile.
	ModelRequestStarted EventType = "model_request_started"
	ModelRequestUnknown EventType = "model_request_unknown"
	ModelFailed         EventType = "model_failed"
	ModelCompleted      EventType = "model_completed"
	ModelStreamed       EventType = "model_streamed"
	AssistantMessage    EventType = "assistant_message"
	PlanUpdated         EventType = "plan_updated"
	ToolCallStarted     EventType = "tool_call_started"
	ToolCallCompleted   EventType = "tool_call_completed"
	ToolCallFailed      EventType = "tool_call_failed"
	ApprovalRequested   EventType = "approval_requested"
	ApprovalResolved    EventType = "approval_resolved"
	CompactionCreated   EventType = "compaction_created"
	TurnCompleted       EventType = "turn_completed"
	TurnFailed          EventType = "turn_failed"
	TurnCanceled        EventType = "turn_canceled"
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
	Plan       *tool.PlanUpdate   `json:"plan,omitempty"`
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

type Fanout struct {
	mu    sync.RWMutex
	sinks []Sink
}

func NewFanout(sinks ...Sink) *Fanout { return &Fanout{sinks: append([]Sink(nil), sinks...)} }
func (f *Fanout) Add(sink Sink) {
	if sink == nil {
		return
	}
	f.mu.Lock()
	f.sinks = append(f.sinks, sink)
	f.mu.Unlock()
}
func (f *Fanout) Publish(ctx context.Context, event Event) {
	f.mu.RLock()
	sinks := append([]Sink(nil), f.sinks...)
	f.mu.RUnlock()
	for _, sink := range sinks {
		sink.Publish(ctx, event)
	}
}

// AsyncSink decouples non-authoritative projections from the agent turn. The
// transcript has already committed before a sink receives an event, so an
// overloaded projection can be rebuilt by replay rather than delaying model
// streaming or tool execution.
type AsyncSink struct {
	sink    Sink
	queue   chan Event
	shed    chan struct{}
	dropped atomic.Uint64
}

func NewAsyncSink(ctx context.Context, sink Sink, capacity int) *AsyncSink {
	if capacity <= 0 {
		capacity = 1024
	}
	s := &AsyncSink{sink: sink, queue: make(chan Event, capacity), shed: make(chan struct{}, 1)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-s.queue:
				if s.sink != nil {
					s.sink.Publish(context.WithoutCancel(ctx), event)
				}
			}
		}
	}()
	return s
}

func (s *AsyncSink) Publish(_ context.Context, event Event) {
	if s == nil || s.sink == nil || event.Type == ModelStreamed {
		return
	}
	select {
	case s.queue <- event:
	default:
		// This sink is only for rebuildable projections. Bounded loss is safer
		// than an unbounded detached goroutine per event: an unavailable
		// projection must not exhaust the worker that owns the canonical
		// transcript. Consumers recover missing events from that transcript.
		s.dropped.Add(1)
		select {
		case s.shed <- struct{}{}:
		default:
		}
	}
}

// Dropped reports projection events deliberately shed because the sink was
// saturated. It is intended for health/metrics wiring; canonical events are
// never dropped by this component.
func (s *AsyncSink) Dropped() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// Shed is signaled (coalesced) when Publish drops a projection event because
// the queue is saturated. Consumers should replay canonical state rather than
// attempting to recover the individual event.
func (s *AsyncSink) Shed() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.shed
}
