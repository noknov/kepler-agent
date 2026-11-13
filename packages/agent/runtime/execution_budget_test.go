package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestTurnLimitsCannotExpandOperatorBudget(t *testing.T) {
	for _, test := range []struct {
		name                        string
		configured, requested, want int
	}{{"requested", 5, 2, 2}, {"operator", 2, 10, 2}, {"inherit", 2, 0, 2}, {"bounded unlimited", UnlimitedSteps, 2, 2}} {
		t.Run(test.name, func(t *testing.T) {
			store := transcript.NewMemoryStore()
			client := &longRunningModel{rounds: 10}
			catalog, _ := tool.NewCatalog(echoTool{})
			runner, err := New(Config{Model: "test", MaxSteps: test.configured}, Dependencies{Model: client, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "work"), Limits: TurnLimits{MaxSteps: test.requested}, WorkClass: model.WorkBackground})
			if err == nil || result.Termination != TerminationMaxSteps || client.calls != test.want {
				t.Fatalf("expanded step budget: result=%+v calls=%d err=%v", result, client.calls, err)
			}
			events, err := store.Load(t.Context(), "s", 0)
			if err != nil {
				t.Fatal(err)
			}
			var start struct {
				Budget executionBudget `json:"execution_budget"`
			}
			for _, event := range events {
				if event.Type == transcript.TurnStarted {
					if err := json.Unmarshal(event.Metadata, &start); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if start.Budget.MaxSteps != test.want || start.Budget.Class != model.WorkBackground {
				t.Fatalf("execution contract not captured: %+v", start)
			}
		})
	}
}

func TestRestartCannotRefreshCapturedExecutionBudget(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata string
		step     int
		reason   TerminationReason
	}{{"steps", `{"execution_budget":{"max_steps":2,"max_total_tokens":0,"class":"background"}}`, 2, TerminationMaxSteps}, {"tokens", `{"execution_budget":{"max_steps":10,"max_total_tokens":100,"class":"background"}}`, 0, TerminationBudgetExhausted}} {
		t.Run(test.name, func(t *testing.T) {
			store := transcript.NewMemoryStore()
			events := []transcript.Event{{Type: transcript.TurnStarted, Metadata: json.RawMessage(test.metadata)}, {Type: transcript.UserInput, Message: func() *model.Message { m := model.TextMessage(model.RoleUser, "work"); return &m }()}}
			if test.step > 0 {
				raw, _ := json.Marshal(map[string]int{"step": test.step})
				events = append(events, transcript.Event{Type: transcript.StepStarted, Metadata: raw})
			}
			for _, event := range events {
				event.SessionID = "s"
				event.TurnID = "t"
				if _, err := store.Append(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			client := &scriptedModel{}
			catalog, _ := tool.NewCatalog()
			runner, err := New(Config{Model: "test", MaxSteps: 20, MaxTotalTokens: 10000, MaxOutputTokens: 100}, Dependencies{Model: client, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "resume"), Limits: TurnLimits{MaxSteps: 100, MaxTotalTokens: 100000}, WorkClass: model.WorkInteractive})
			if err == nil || result.Termination != test.reason || len(client.requests) != 0 {
				t.Fatalf("restart refreshed budget: result=%+v calls=%d err=%v", result, len(client.requests), err)
			}
		})
	}
}

func TestInheritedTokenBudgetCannotBeReplacedByTurnLimits(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	client := &scriptedModel{}
	store := transcript.NewMemoryStore()
	runner, err := New(Config{MaxTotalTokens: 1000, MaxOutputTokens: 100}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx := model.WithBudget(context.Background(), 500)
	_, err = runner.RunTurn(ctx, TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "work"), Limits: TurnLimits{MaxTotalTokens: 100}})
	if err == nil || len(client.requests) != 0 || model.TokenBudgetLimit(ctx) != 500 {
		t.Fatal("smaller child ceiling was silently ignored or replaced parent consumption")
	}
}

func TestFiniteTokenBudgetRequiresFiniteModelOutput(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	if _, err := New(Config{MaxTotalTokens: 100}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()}); err == nil {
		t.Fatal("finite token budget admitted an unbounded provider output")
	}
	runner, err := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "work"), Limits: TurnLimits{MaxTotalTokens: 100}})
	if err == nil {
		t.Fatal("per-turn token budget admitted unbounded output")
	}
}
