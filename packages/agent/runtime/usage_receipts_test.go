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

type fixtureEmbedder struct{}

func (fixtureEmbedder) Embed(context.Context, model.EmbeddingRequest) (model.EmbeddingResponse, error) {
	return model.EmbeddingResponse{Vectors: [][]float64{{1, 0}}, Usage: model.Usage{Reported: true, InputTokens: 7}}, nil
}
func TestEmbeddingUsageIsSeparateAndSurvivesReplay(t *testing.T) {
	store := transcript.NewMemoryStore()
	catalog, _ := tool.NewCatalog()
	raw := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 3}}}}
	runner, err := New(Config{MaxTotalTokens: 10000}, Dependencies{Model: raw, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	request := TurnRequest{SessionID: "embedding-ledger", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "hi"), PrepareContext: func(ctx context.Context, _ tool.Scope) (PreparedContext, error) {
		_, err := model.GovernEmbedder(fixtureEmbedder{}, nil).Embed(ctx, model.EmbeddingRequest{Model: "embedding-model", Input: []string{"hi"}, InputTokenEstimate: 10})
		return PreparedContext{}, err
	}}
	result, err := runner.RunTurn(context.Background(), request)
	if err != nil || result.Usage.InputTokens != 3 || result.EmbeddingUsage.InputTokens != 7 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	request.PrepareContext = func(context.Context, tool.Scope) (PreparedContext, error) {
		t.Fatal("replay called embeddings")
		return PreparedContext{}, nil
	}
	replay, err := runner.RunTurn(context.Background(), request)
	if err != nil || replay.Usage != result.Usage || replay.EmbeddingUsage != result.EmbeddingUsage {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}
