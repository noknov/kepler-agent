package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type longRunningModel struct{ calls, rounds int }

func (m *longRunningModel) Generate(_ context.Context, _ model.Request, _ model.EventSink) (model.Response, error) {
	m.calls++
	if m.calls > m.rounds {
		return model.Response{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}, nil
	}
	call := model.ToolCall{ID: fmt.Sprintf("call-%d", m.calls), Name: "echo", Arguments: json.RawMessage(`{}`)}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls}, nil
}

func TestUnlimitedStepsCanCompleteBeyondDefaultLimit(t *testing.T) {
	client := &longRunningModel{rounds: 257}
	catalog, _ := tool.NewCatalog(echoTool{})
	runner, err := New(Config{Model: "test", MaxSteps: UnlimitedSteps}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "unlimited", Input: model.TextMessage(model.RoleUser, "work")})
	if err != nil || result.Termination != TerminationCompleted || result.Steps != 258 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, client.calls)
	}
}

func TestUnlimitedStepsStillHonorsDeadline(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Model: "test", MaxSteps: UnlimitedSteps}, Dependencies{Model: blockingModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := runner.RunTurn(ctx, TurnRequest{SessionID: "deadline", Input: model.TextMessage(model.RoleUser, "work")})
	if !errors.Is(err, context.DeadlineExceeded) || result.Termination == TerminationMaxSteps {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestStepBudgetDefaultsAndValidation(t *testing.T) {
	if got := (Config{}).withDefaults().MaxSteps; got != 256 {
		t.Fatalf("default steps=%d", got)
	}
	catalog, _ := tool.NewCatalog()
	if _, err := New(Config{MaxSteps: -2}, Dependencies{Model: &longRunningModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()}); err == nil {
		t.Fatal("invalid negative step budget accepted")
	}
}
