package hosted

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/runs"
)

type projectionStore struct {
	mu   sync.Mutex
	runs map[string]runs.Run
}

func newProjectionStore() *projectionStore { return &projectionStore{runs: make(map[string]runs.Run)} }
func (s *projectionStore) Save(_ context.Context, run runs.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return nil
}
func (s *projectionStore) Get(_ context.Context, id string) (runs.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	run.Steps = append([]runs.Step(nil), run.Steps...)
	return run, ok, nil
}
func (s *projectionStore) AppendStep(_ context.Context, id string, step runs.Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	for _, existing := range run.Steps {
		if existing.ID == step.ID {
			return nil
		}
	}
	run.Steps = append(run.Steps, step)
	s.runs[id] = run
	return nil
}
func (*projectionStore) List(context.Context, int) ([]runs.Run, error)             { return nil, nil }
func (*projectionStore) ListBySession(context.Context, string) ([]runs.Run, error) { return nil, nil }
func (*projectionStore) UserAuditSummaries(context.Context, time.Time, time.Time) ([]runs.UserAuditSummary, error) {
	return nil, nil
}
func (*projectionStore) AddFeedback(context.Context, string, runs.Feedback) error { return nil }
func (*projectionStore) AddFeedbackForMessage(context.Context, string, string, runs.Feedback) (string, bool, error) {
	return "", false, nil
}

func TestRunSinkReplayIsIdempotentAndProjectsFailures(t *testing.T) {
	store := newProjectionStore()
	sink := &RunSink{Store: store, Provider: "test", Model: "model"}
	start := time.Unix(100, 0).UTC()
	startedMetadata, _ := json.Marshal(map[string]any{"user_id": "U1", "scope": map[string]string{"channel": "C1"}, "model": "model"})
	failedMetadata, _ := json.Marshal(map[string]any{"attempt": 1, "kind": model.ErrorUnavailable, "retryable": true})
	usageMetadata, _ := json.Marshal(map[string]any{"usage": model.Usage{InputTokens: 10, OutputTokens: 2}, "finish_reason": model.FinishStop})
	events := []transcript.Event{
		{ID: "turn-start", SessionID: "s", TurnID: "t", Type: transcript.TurnStarted, Timestamp: start, Metadata: startedMetadata},
		{ID: "request-1", SessionID: "s", TurnID: "t", Type: transcript.ModelRequested, Timestamp: start.Add(time.Second)},
		{ID: "failed-1", SessionID: "s", TurnID: "t", Type: transcript.ModelFailed, Timestamp: start.Add(2 * time.Second), Error: "overloaded", Metadata: failedMetadata},
		{ID: "request-2", SessionID: "s", TurnID: "t", Type: transcript.ModelRequested, Timestamp: start.Add(3 * time.Second)},
		{ID: "completed-2", SessionID: "s", TurnID: "t", Type: transcript.ModelCompleted, Timestamp: start.Add(4 * time.Second), Metadata: usageMetadata},
		{ID: "answer", SessionID: "s", TurnID: "t", Type: transcript.AssistantMessage, Timestamp: start.Add(4 * time.Second), Message: ptrMessage(model.TextMessage(model.RoleAssistant, "done"))},
		{ID: "turn-done", SessionID: "s", TurnID: "t", Type: transcript.TurnCompleted, Timestamp: start.Add(5 * time.Second), Status: "completed"},
	}
	for repeat := 0; repeat < 2; repeat++ {
		for _, event := range events {
			sink.publish(context.Background(), event, false)
		}
	}
	run, ok, _ := store.Get(context.Background(), "t")
	if !ok || run.Status != "completed" || len(run.Steps) != 2 {
		t.Fatalf("run=%+v", run)
	}
	if run.Steps[0].Error != "overloaded" || run.Usage.TotalTokens != 12 || run.FinalHash == "" {
		t.Fatalf("projection lost failure, usage, or final: %+v", run)
	}
}

func TestToolErrorIncludesToolFailureDetail(t *testing.T) {
	event := transcript.Event{
		ToolResult: &tool.Result{
			Content:   []model.Content{{Type: model.ContentText, Text: "gopls is not installed"}},
			IsError:   true,
			ErrorCode: "tool_error",
		},
	}
	if got := toolError(event); got != "gopls is not installed" {
		t.Fatalf("toolError() = %q", got)
	}
}

func TestToolErrorIgnoresSuccessfulArtifactSpill(t *testing.T) {
	event := transcript.Event{
		ToolResult: &tool.Result{
			Content: []model.Content{{Type: model.ContentText, Text: "Tool output was stored as artifact spill://run/artifact"}},
		},
	}
	if got := toolError(event); got != "" {
		t.Fatalf("toolError() = %q, want empty for successful spill", got)
	}
}

func ptrMessage(message model.Message) *model.Message { return &message }
