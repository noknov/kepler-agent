package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestRuntimePersistsEachActualAttemptUsage(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	raw := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 3}}}}
	resilient := &model.ResilientClient{Primary: raw, PrimaryProvider: "test", MaxAttempts: 1}
	runner, err := New(Config{MaxTotalTokens: 10000}, Dependencies{Model: resilient, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), TurnRequest{SessionID: "ledger", Input: model.TextMessage(model.RoleUser, "hello")}); err != nil {
		t.Fatal(err)
	}
	events, _ := store.Load(context.Background(), "ledger", 0)
	count := 0
	for _, event := range events {
		if event.Type == transcript.ModelUsageSettled {
			count++
			var receipt model.AttemptUsage
			if err := json.Unmarshal(event.Metadata, &receipt); err != nil || receipt.Charged != 3 || receipt.Provider != "test" || receipt.Class != model.WorkInteractive {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
		}
	}
	if count != 1 {
		t.Fatalf("usage receipts=%d", count)
	}
}
