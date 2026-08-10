package hosted

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/runs"
)

// RunSink materializes canonical runtime events into the existing run index.
// The transcript remains authoritative; this sink is a query projection.
type RunSink struct {
	Store    runs.Store
	Provider string
	Model    string
	Rates    observability.CostRates
	Metrics  *observability.Recorder

	mu     sync.Mutex
	active map[string]*runState
}

type runState struct {
	observer   *runs.Observer
	modelStart time.Time
	final      string
}

func (s *RunSink) Publish(_ context.Context, event transcript.Event) {
	if s == nil || event.TurnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = make(map[string]*runState)
	}
	state := s.active[event.TurnID]
	switch event.Type {
	case transcript.TurnStarted:
		var metadata struct {
			UserID string            `json:"user_id"`
			Scope  map[string]string `json:"scope"`
			Model  string            `json:"model"`
		}
		_ = json.Unmarshal(event.Metadata, &metadata)
		modelName := metadata.Model
		if modelName == "" {
			modelName = s.Model
		}
		state = &runState{observer: runs.NewObserver(s.Store, runs.Run{
			ID: event.TurnID, SessionID: event.SessionID, EventID: event.TurnID,
			UserID: metadata.UserID, Channel: metadata.Scope["channel"], ThreadTS: metadata.Scope["thread_ts"],
			Provider: s.Provider, Model: modelName, StartedAt: event.Timestamp,
		}, s.Rates)}
		s.active[event.TurnID] = state
	case transcript.ModelRequested:
		if state != nil {
			state.modelStart = event.Timestamp
		}
	case transcript.ModelCompleted:
		if state == nil {
			return
		}
		var metadata struct {
			Usage        model.Usage        `json:"usage"`
			FinishReason model.FinishReason `json:"finish_reason"`
		}
		_ = json.Unmarshal(event.Metadata, &metadata)
		usage := recordedUsage(metadata.Usage)
		duration := event.Timestamp.Sub(state.modelStart)
		state.observer.LLMResponse(llm.Response{Usage: usage, FinishReason: string(metadata.FinishReason)}, duration, nil)
		if s.Metrics != nil {
			s.Metrics.LLMCall(usage, duration, nil)
		}
	case transcript.AssistantMessage:
		if state != nil && event.Message != nil && len(event.Message.ToolCalls()) == 0 {
			state.final = event.Message.Text()
		}
	case transcript.ToolCallCompleted, transcript.ToolCallFailed:
		if state == nil || event.ToolCall == nil {
			return
		}
		var duration time.Duration
		if event.ToolResult != nil && event.ToolResult.Metadata != nil {
			if value, ok := event.ToolResult.Metadata["duration_ms"].(int64); ok {
				duration = time.Duration(value) * time.Millisecond
			}
			if value, ok := event.ToolResult.Metadata["duration_ms"].(float64); ok {
				duration = time.Duration(value) * time.Millisecond
			}
		}
		var stepErr error
		if event.Type == transcript.ToolCallFailed {
			stepErr = runEventError(event)
		}
		state.observer.ToolCallWithMetadata(event.ToolCall.Name, event.ToolCall.Arguments, duration, stepErr)
		if s.Metrics != nil {
			s.Metrics.ToolCall(event.ToolCall.Name, duration, stepErr)
		}
	case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
		if state == nil {
			return
		}
		status := "completed"
		var finishErr error
		if event.Type == transcript.TurnCanceled {
			status, finishErr = "canceled", runEventError(event)
		}
		if event.Type == transcript.TurnFailed {
			status, finishErr = "error", runEventError(event)
		}
		state.observer.Finish(status, "", finishErr, state.final)
		delete(s.active, event.TurnID)
	}
}

func recordedUsage(value model.Usage) llm.Usage {
	return llm.Usage{PromptTokens: int(value.InputTokens), CompletionTokens: int(value.OutputTokens), TotalTokens: int(value.InputTokens + value.OutputTokens), CacheReadInputTokens: int(value.CacheReadTokens), CacheCreationInputTokens: int(value.CacheCreatedTokens)}
}

type eventError string

func (e eventError) Error() string { return string(e) }
func runEventError(event transcript.Event) error {
	if event.Error != "" {
		return eventError(event.Error)
	}
	return eventError(event.Status)
}
