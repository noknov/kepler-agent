package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
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
	mu         sync.Mutex
	modelStart time.Time
	final      string
}

func (s *RunSink) LinkSlackMessage(ctx context.Context, turnID, channel, messageTS string) error {
	if s == nil || s.Store == nil || turnID == "" || channel == "" || messageTS == "" {
		return fmt.Errorf("complete Slack delivery link is required")
	}
	run, ok, err := s.Store.Get(ctx, turnID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("run %q not found", turnID)
	}
	run.SlackChannel = channel
	run.SlackMessageTS = messageTS
	return s.Store.Save(ctx, run)
}

func (s *RunSink) SlackMessageDelivered(ctx context.Context, turnID string) (bool, error) {
	if s == nil || s.Store == nil || turnID == "" {
		return false, nil
	}
	run, ok, err := s.Store.Get(ctx, turnID)
	return ok && run.SlackChannel != "" && run.SlackMessageTS != "", err
}

func (s *RunSink) Publish(ctx context.Context, event transcript.Event) {
	s.publish(ctx, event, true)
}

func (s *RunSink) publish(ctx context.Context, event transcript.Event, liveMetrics bool) {
	if s == nil || s.Store == nil || event.TurnID == "" {
		return
	}
	if event.Type == transcript.ModelStreamed {
		return
	}
	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[string]*runState)
	}
	state := s.active[event.TurnID]
	if event.Type == transcript.TurnStarted && state == nil {
		state = &runState{}
		s.active[event.TurnID] = state
	}
	s.mu.Unlock()
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
	}
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
		if err := s.saveRun(ctx, existing); err != nil {
			log.Printf("project agent run start %s: %v", event.TurnID, err)
		}
		if liveMetrics && s.Metrics != nil {
			s.Metrics.Request()
		}
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
		if err := s.appendStep(ctx, event.TurnID, step); err != nil {
			log.Printf("project model step %s: %v", event.ID, err)
		}
		if liveMetrics && s.Metrics != nil {
			s.Metrics.LLMCall(usage, duration, nil)
		}
	case transcript.ModelFailed:
		if state == nil {
			return
		}
		duration := event.Timestamp.Sub(state.modelStart)
		step := runs.Step{ID: event.ID, SpanID: event.ID, Type: "llm", Name: s.modelFor(ctx, event.TurnID), StartedAt: state.modelStart, DurationMS: duration.Milliseconds(), Error: event.Error, Metadata: rawMetadata(event.Metadata)}
		if err := s.appendStep(ctx, event.TurnID, step); err != nil {
			log.Printf("project failed model step %s: %v", event.ID, err)
		}
		if liveMetrics && s.Metrics != nil {
			var stepErr error
			if event.Error != context.Canceled.Error() && event.Error != context.DeadlineExceeded.Error() {
				stepErr = fmt.Errorf("%s", event.Error)
			}
			s.Metrics.LLMCall(llm.Usage{}, duration, stepErr)
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
		if err := s.appendStep(ctx, event.TurnID, step); err != nil {
			log.Printf("project tool step %s: %v", event.ID, err)
		}
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
		run.Termination = event.Status
		run.Status = "completed"
		if event.Status == string(agentruntimeTerminationPendingInput) {
			run.Status = "pending_user"
		} else if event.Type == transcript.TurnCompleted && event.Status != "" && event.Status != "completed" {
			run.Status = "incomplete"
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
		if err := s.saveRun(ctx, run); err != nil {
			log.Printf("project agent run terminal %s: %v", event.TurnID, err)
		}
		if liveMetrics && s.Metrics != nil {
			s.Metrics.Latency(time.Duration(run.DurationMS) * time.Millisecond)
		}
		s.mu.Lock()
		delete(s.active, event.TurnID)
		s.mu.Unlock()
	}
}

func (s *RunSink) saveRun(ctx context.Context, run runs.Run) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.Store.Save(ctx, run); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}

// Recover replays transcript events for runs left in running state. Projection
// writes are idempotent because every generated step uses its event ID.
func (s *RunSink) Recover(ctx context.Context, pool *pgxpool.Pool) error {
	if s == nil || pool == nil {
		return nil
	}
	rows, err := pool.Query(ctx, `SELECT t.payload FROM agent_transcript_events t
LEFT JOIN agent_runs r ON r.id=t.turn_id
WHERE t.turn_id<>'' AND (r.id IS NULL OR r.payload->>'status'='running')
ORDER BY t.turn_id,t.sequence`)
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

func (s *RunSink) appendStep(ctx context.Context, runID string, step runs.Step) error {
	if store, ok := s.Store.(runs.StepStore); ok {
		if err := store.AppendStep(ctx, runID, step); err != nil {
			return err
		}
	} else if run, exists, err := s.Store.Get(ctx, runID); err != nil {
		return err
	} else if exists {
		seen := false
		for _, existing := range run.Steps {
			seen = seen || existing.ID == step.ID
		}
		if !seen {
			run.Steps = append(run.Steps, step)
			if err := s.saveRun(ctx, run); err != nil {
				return err
			}
		}
	}
	if run, exists, err := s.Store.Get(ctx, runID); err != nil {
		return err
	} else if exists {
		recomputeRun(&run)
		return s.saveRun(ctx, run)
	}
	return nil
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
	return llm.Usage{PromptTokens: int(value.InputTokens), CompletionTokens: int(value.OutputTokens), TotalTokens: int(value.InputTokens + value.OutputTokens), CacheReadInputTokens: int(value.CacheReadTokens), CacheCreationInputTokens: int(value.CacheCreatedTokens), CacheIncludedInPrompt: value.CacheTokensIncludedInInput}
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
	if event.ToolResult != nil {
		if !event.ToolResult.IsError && event.ToolResult.ErrorCode == "" {
			return ""
		}
		for _, content := range event.ToolResult.Content {
			if content.Type == model.ContentText {
				if text := strings.TrimSpace(content.Text); text != "" {
					if len(text) > 1000 {
						return text[:1000] + "..."
					}
					return text
				}
			}
		}
		if event.ToolResult.ErrorCode != "" {
			return event.ToolResult.ErrorCode
		}
	}
	return event.Status
}
