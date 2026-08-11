package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/model"
	"github.com/noknov/slack-copilot-agent/packages/agentv2/transcript"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/runs"
)

// RunSink is an idempotent query projection of canonical transcript events.
// Event IDs become step IDs, so replay after a worker restart cannot duplicate
// model or tool steps. The transcript remains the source of truth.
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
	modelStart time.Time
	final      string
}

func (s *RunSink) Publish(ctx context.Context, event transcript.Event) {
	s.publish(ctx, event, true)
}

func (s *RunSink) publish(ctx context.Context, event transcript.Event, liveMetrics bool) {
	if s == nil || s.Store == nil || event.TurnID == "" {
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
		existing, ok, _ := s.Store.Get(ctx, event.TurnID)
		if !ok {
			existing = runs.Run{ID: event.TurnID, TraceID: runs.NewTraceID(), SessionID: event.SessionID, EventID: event.TurnID, StartedAt: event.Timestamp}
		}
		existing.UserID, existing.Channel, existing.ThreadTS = metadata.UserID, metadata.Scope["channel"], metadata.Scope["thread_ts"]
		existing.Provider, existing.Model, existing.Status = s.Provider, modelName, "running"
		_ = s.Store.Save(ctx, existing)
		state = &runState{}
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
		step := runs.Step{ID: event.ID, SpanID: event.ID, Type: "llm", Name: s.modelFor(ctx, event.TurnID), StartedAt: state.modelStart, DurationMS: duration.Milliseconds(), Usage: usage, FinishReason: string(metadata.FinishReason), EstimatedCostUSD: s.Rates.EstimateUSD(usage)}
		s.appendStep(ctx, event.TurnID, step)
		if liveMetrics && s.Metrics != nil {
			s.Metrics.LLMCall(usage, duration, nil)
		}
	case transcript.ModelFailed:
		if state == nil {
			return
		}
		duration := event.Timestamp.Sub(state.modelStart)
		step := runs.Step{ID: event.ID, SpanID: event.ID, Type: "llm", Name: s.modelFor(ctx, event.TurnID), StartedAt: state.modelStart, DurationMS: duration.Milliseconds(), Error: event.Error, Metadata: rawMetadata(event.Metadata)}
		s.appendStep(ctx, event.TurnID, step)
		if liveMetrics && s.Metrics != nil {
			s.Metrics.LLMCall(llm.Usage{}, duration, fmt.Errorf("%s", event.Error))
		}
	case transcript.AssistantMessage:
		if state != nil && event.Message != nil && len(event.Message.ToolCalls()) == 0 {
			state.final = event.Message.Text()
		}
	case transcript.ToolCallCompleted, transcript.ToolCallFailed:
		if state == nil || event.ToolCall == nil {
			return
		}
		duration := toolDuration(event)
		step := runs.Step{ID: event.ID, SpanID: event.ID, Type: "tool", Name: event.ToolCall.Name, StartedAt: event.Timestamp.Add(-duration), DurationMS: duration.Milliseconds(), Metadata: toolStepMetadata(event)}
		if event.Type == transcript.ToolCallFailed {
			step.Error = toolError(event)
		}
		s.appendStep(ctx, event.TurnID, step)
		if liveMetrics && s.Metrics != nil {
			var stepErr error
			if step.Error != "" {
				stepErr = fmt.Errorf("%s", step.Error)
			}
			s.Metrics.ToolCall(event.ToolCall.Name, duration, stepErr)
		}
	case transcript.TurnCompleted, transcript.TurnFailed, transcript.TurnCanceled:
		if state == nil {
			return
		}
		run, ok, _ := s.Store.Get(ctx, event.TurnID)
		if !ok {
			return
		}
		run.Status = "completed"
		if event.Status == string(agentruntimeTerminationPendingInput) {
			run.Status = "pending_user"
		}
		if event.Type == transcript.TurnCanceled {
			run.Status = "canceled"
		}
		if event.Type == transcript.TurnFailed {
			run.Status = "error"
		}
		run.Error = event.Error
		run.EndedAt = event.Timestamp
		run.DurationMS = event.Timestamp.Sub(run.StartedAt).Milliseconds()
		if state.final != "" {
			run.FinalHash = runs.HashText(state.final)
		}
		recomputeRun(&run)
		run.Quality = runs.Score(run)
		_ = s.Store.Save(ctx, run)
		delete(s.active, event.TurnID)
	}
}

// Recover replays transcript events for runs left in running state. Projection
// writes are idempotent because every generated step uses its event ID.
func (s *RunSink) Recover(ctx context.Context, pool *pgxpool.Pool) error {
	if s == nil || pool == nil {
		return nil
	}
	rows, err := pool.Query(ctx, `SELECT t.payload FROM agent_transcript_events t JOIN agent_runs r ON r.id=t.turn_id WHERE r.payload->>'status'='running' ORDER BY t.turn_id,t.sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var event transcript.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		s.publish(ctx, event, false)
	}
	return rows.Err()
}

type terminationMarker string

const agentruntimeTerminationPendingInput terminationMarker = "pending_input"

func (s *RunSink) appendStep(ctx context.Context, runID string, step runs.Step) {
	if store, ok := s.Store.(runs.StepStore); ok {
		_ = store.AppendStep(ctx, runID, step)
	} else if run, exists, _ := s.Store.Get(ctx, runID); exists {
		seen := false
		for _, existing := range run.Steps {
			seen = seen || existing.ID == step.ID
		}
		if !seen {
			run.Steps = append(run.Steps, step)
			_ = s.Store.Save(ctx, run)
		}
	}
	if run, exists, _ := s.Store.Get(ctx, runID); exists {
		recomputeRun(&run)
		_ = s.Store.Save(ctx, run)
	}
}

func (s *RunSink) modelFor(ctx context.Context, runID string) string {
	if run, ok, _ := s.Store.Get(ctx, runID); ok && run.Model != "" {
		return run.Model
	}
	return s.Model
}

func recomputeRun(run *runs.Run) {
	run.Usage = llm.Usage{}
	run.EstimatedCostUSD = 0
	for _, step := range run.Steps {
		if step.Type != "llm" {
			continue
		}
		run.Usage.PromptTokens += step.Usage.PromptTokens
		run.Usage.CompletionTokens += step.Usage.CompletionTokens
		run.Usage.TotalTokens += step.Usage.TotalTokens
		run.Usage.CacheReadInputTokens += step.Usage.CacheReadInputTokens
		run.Usage.CacheCreationInputTokens += step.Usage.CacheCreationInputTokens
		run.Usage.ReasoningTokens += step.Usage.ReasoningTokens
		run.EstimatedCostUSD += step.EstimatedCostUSD
	}
}

func recordedUsage(value model.Usage) llm.Usage {
	return llm.Usage{PromptTokens: int(value.InputTokens), CompletionTokens: int(value.OutputTokens), TotalTokens: int(value.InputTokens + value.OutputTokens), CacheReadInputTokens: int(value.CacheReadTokens), CacheCreationInputTokens: int(value.CacheCreatedTokens)}
}

func rawMetadata(raw json.RawMessage) map[string]any {
	var metadata map[string]any
	_ = json.Unmarshal(raw, &metadata)
	return metadata
}

func toolDuration(event transcript.Event) time.Duration {
	if event.ToolResult != nil && event.ToolResult.Metadata != nil {
		switch value := event.ToolResult.Metadata["duration_ms"].(type) {
		case int64:
			return time.Duration(value) * time.Millisecond
		case float64:
			return time.Duration(value) * time.Millisecond
		case int:
			return time.Duration(value) * time.Millisecond
		}
	}
	return 0
}

func toolStepMetadata(event transcript.Event) map[string]any {
	metadata := map[string]any{"call_id": event.ToolCall.ID}
	if len(event.ToolCall.Arguments) > 0 {
		metadata["args_bytes"] = len(event.ToolCall.Arguments)
	}
	if event.ToolResult != nil {
		metadata["error_code"] = event.ToolResult.ErrorCode
		metadata["truncated"] = event.ToolResult.Truncated
	}
	return metadata
}

func toolError(event transcript.Event) string {
	if event.Error != "" {
		return event.Error
	}
	if event.ToolResult != nil && event.ToolResult.ErrorCode != "" {
		return event.ToolResult.ErrorCode
	}
	return event.Status
}
