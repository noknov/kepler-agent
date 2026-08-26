package runs

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/llm"
	"github.com/noknov/kepler-agent/packages/observability"
)

func TestObserverRecordsTraceMetadataAndStack(t *testing.T) {
	store := newTestRunStore()
	observer := NewObserver(store, Run{Model: "test-model"}, observability.CostRates{})
	observer.ToolCallWithMetadata("code-read_file", json.RawMessage(`{"path":"app.go","max_lines":20}`), time.Millisecond, nil)
	observer.RecordErrorStack("stack trace")
	observer.Finish("error", "err-1", assertErr("boom"), "")

	run, ok, err := store.Get(context.Background(), observer.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected run to be saved")
	}
	if run.TraceID == "" || len(run.Steps) != 1 || run.Steps[0].SpanID == "" || run.Steps[0].ParentSpanID != run.TraceID {
		t.Fatalf("missing trace metadata: %#v", run)
	}
	if run.ErrorStack != "stack trace" {
		t.Fatalf("ErrorStack = %q", run.ErrorStack)
	}
	if run.Steps[0].Metadata["args_hash"] == "" {
		t.Fatalf("missing args hash: %#v", run.Steps[0].Metadata)
	}
}

type failingStepStore struct{ *testRunStore }

func (f failingStepStore) AppendStep(context.Context, string, Step) error {
	return errors.New("step store unavailable")
}

func TestObserverReportsPersistenceErrors(t *testing.T) {
	files := newTestRunStore()
	observer := NewObserver(failingStepStore{files}, Run{SessionID: "thread-1", Model: "test"}, observability.CostRates{})
	observer.ToolCall("code-search", time.Millisecond, nil)
	observer.Finish("completed", "", nil, "done")
	if observer.PersistenceError() == nil {
		t.Fatal("expected append failure to be retained")
	}
}

func TestObserverRecordsLLMResponseOutputWithoutReasoning(t *testing.T) {
	store := newTestRunStore()
	observer := NewObserver(store, Run{Model: "test-model"}, observability.CostRates{})
	observer.LLMResponse(llm.Response{
		Message: llm.Message{
			Content:          "final answer",
			ReasoningContent: "private-ish reasoning excerpt",
			ToolCalls: []llm.ToolCall{{
				Function: llm.ToolFunction{Name: "code-search"},
			}},
		},
		FinishReason: "stop",
		Usage:        llm.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13},
	}, time.Millisecond, nil)

	run, ok, err := store.Get(context.Background(), observer.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(run.Steps) != 1 {
		t.Fatalf("expected one saved step: %#v", run)
	}
	step := run.Steps[0]
	if step.Content != "final answer" {
		t.Fatalf("Content = %q", step.Content)
	}
	if step.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q", step.FinishReason)
	}
	if len(step.ToolCallNames) != 1 || step.ToolCallNames[0] != "code-search" {
		t.Fatalf("ToolCallNames = %#v", step.ToolCallNames)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

type testRunStore struct {
	mu   sync.Mutex
	runs map[string]Run
}

func newTestRunStore() *testRunStore { return &testRunStore{runs: map[string]Run{}} }

func (s *testRunStore) Save(_ context.Context, run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return nil
}

func (s *testRunStore) Get(_ context.Context, id string) (Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	return run, ok, nil
}

func (s *testRunStore) List(context.Context, int) ([]Run, error)             { return nil, nil }
func (s *testRunStore) ListBySession(context.Context, string) ([]Run, error) { return nil, nil }
func (s *testRunStore) UserAuditSummaries(context.Context, time.Time, time.Time) ([]UserAuditSummary, error) {
	return nil, nil
}
func (s *testRunStore) AddFeedback(context.Context, string, Feedback) error { return nil }
func (s *testRunStore) AddFeedbackForMessage(context.Context, string, string, Feedback) (string, bool, error) {
	return "", false, nil
}
func (s *testRunStore) AppendStep(_ context.Context, runID string, step Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	run.Steps = append(run.Steps, step)
	s.runs[runID] = run
	return nil
}
