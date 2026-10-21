package runtime

import (
	"context"
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"testing"
)

func TestInterruptedTurnRetainsStepsUsageAndTokenBudget(t *testing.T) {
	for _, test := range []struct {
		name   string
		config Config
		reason TerminationReason
	}{
		{"step ceiling", Config{MaxSteps: 2}, TerminationMaxSteps},
		{"unknown token reservation", Config{MaxSteps: 10, MaxTotalTokens: 100, MaxOutputTokens: 20}, TerminationBudgetExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := transcript.NewMemoryStore()
			events := []transcript.Event{
				{Type: transcript.TurnStarted, Metadata: json.RawMessage(`{"user_id":"u"}`)},
				{Type: transcript.StepStarted, Metadata: json.RawMessage(`{"step":2}`)},
				{Type: transcript.ModelCompleted, Metadata: json.RawMessage(`{"usage":{"reported":true,"input_tokens":17,"output_tokens":3}}`)},
				{Type: transcript.TokenBudgetUpdated, Metadata: json.RawMessage(`{"spent":90,"reserved":20}`)},
			}
			for _, event := range events {
				event.SessionID = "s"
				event.TurnID = "t"
				if _, err := store.Append(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			client := &scriptedModel{}
			catalog, _ := tool.NewCatalog()
			runner, err := New(test.config, Dependencies{Model: client, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(ctx, TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "continue"), Scope: tool.Scope{UserID: "u"}})
			if err == nil || result.Termination != test.reason || len(client.requests) != 0 || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 3 {
				t.Fatalf("result=%+v requests=%d err=%v", result, len(client.requests), err)
			}
		})
	}
}
