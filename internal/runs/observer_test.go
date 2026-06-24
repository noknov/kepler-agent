package runs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/observability"
)

func TestObserverRecordsTraceMetadataAndStack(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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

func TestObserverRecordsLLMResponseOutput(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	if step.ReasoningContent != "private-ish reasoning excerpt" {
		t.Fatalf("ReasoningContent = %q", step.ReasoningContent)
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
