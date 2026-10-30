package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestRecoveredUsageMatchesMixedProviderBudget(t *testing.T) {
	inclusive := model.Usage{Reported: true, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40, CacheTokensIncludedInInput: true}
	exclusive := model.Usage{Reported: true, InputTokens: 60, OutputTokens: 10, CacheReadTokens: 20, CacheCreatedTokens: 5}
	for _, usages := range [][]model.Usage{{inclusive, exclusive}, {exclusive, inclusive}} {
		var events []transcript.Event
		for _, usage := range usages {
			raw, _ := json.Marshal(map[string]any{"usage": usage})
			events = append(events, transcript.Event{TurnID: "turn", Type: transcript.ModelCompleted, Metadata: raw})
		}
		progress, err := projectTurnProgress(events, "turn", Config{})
		if err != nil || progress.Budget.Spent != 205 || model.UsageTokens(progress.Usage) != 205 {
			t.Fatalf("aggregate disagrees with exact attempt accounting: usage=%+v budget=%+v err=%v", progress.Usage, progress.Budget, err)
		}
		store := transcript.NewMemoryStore()
		catalog, _ := tool.NewCatalog()
		client := &scriptedModel{responses: []model.Response{
			{FinishReason: model.FinishStop, Usage: usages[0]},
			{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: usages[1]},
		}}
		runner, err := New(Config{MaxEmptyResponseRetries: 1, RetryBaseDelay: time.Nanosecond}, Dependencies{Model: client, Tools: catalog, Transcript: store})
		if err != nil {
			t.Fatal(err)
		}
		request := TurnRequest{SessionID: "mixed", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "inspect")}
		for range 2 {
			result, err := runner.RunTurn(t.Context(), request)
			if err != nil || result.Usage != progress.Usage || len(client.requests) != 2 {
				t.Fatalf("live/replayed usage differs: %+v calls=%d err=%v", result.Usage, len(client.requests), err)
			}
		}
	}
}
