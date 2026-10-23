package runtime

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestContextPreparationCannotOverwriteRecoveredIdentity(t *testing.T) {
	request := TurnRequest{Scope: tool.Scope{UserID: "user", Values: map[string]string{"team_id": "team", "memory_generation": "3"}}, PrepareContext: func(_ context.Context, scope tool.Scope) (PreparedContext, error) {
		scope.Values["team_id"] = "mutated"
		return PreparedContext{Values: map[string]string{"team_id": "other", "memory_generation": "99", "new": "value"}}, nil
	}}
	prepared, err := (&Runtime{}).prepareContext(context.Background(), request)
	if err != nil || prepared.Scope.Values["team_id"] != "team" || prepared.Scope.Values["memory_generation"] != "3" || prepared.Scope.Values["new"] != "value" || request.Scope.Values["team_id"] != "team" {
		t.Fatalf("scope=%+v err=%v", prepared.Scope, err)
	}
}
func TestContextPreparationSharesAdmittedTurnBudget(t *testing.T) {
	ctx := context.Background()
	catalog, _ := tool.NewCatalog()
	client := &scriptedModel{}
	store := transcript.NewMemoryStore()
	runner, err := New(Config{MaxTotalTokens: 10}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(ctx context.Context, _ tool.Scope) (PreparedContext, error) {
		_, err := model.Govern(client, nil).Generate(ctx, model.Request{InputTokenEstimate: 20}, nil)
		return PreparedContext{}, err
	}
	_, err = runner.RunTurn(ctx, TurnRequest{SessionID: "budget", Input: model.TextMessage(model.RoleUser, "hi"), PrepareContext: prepare})
	if model.ErrorKindOf(err) != model.ErrorBudgetExhausted || len(client.requests) != 0 {
		t.Fatalf("preparation bypassed budget: calls=%d err=%v", len(client.requests), err)
	}
	events, _ := store.Load(ctx, "budget", 0)
	found := false
	for _, event := range events {
		found = found || event.Type == transcript.UserInput
	}
	if !found {
		t.Fatal("preparation failure lost original input")
	}
}

func TestPreparedGenerationIsDurableAndCannotRefreshOnReplay(t *testing.T) {
	request := TurnRequest{TurnID: "t", Scope: tool.Scope{Values: map[string]string{"team_id": "team"}}}
	event := transcript.Event{TurnID: "t", Type: transcript.TurnContextPrepared, Metadata: []byte(`{"values":{"team_id":"team","memory_generation":"3"}}`)}
	restored, err := restorePreparedContext(request, []transcript.Event{event})
	if err != nil || restored.Scope.Values["memory_generation"] != "3" {
		t.Fatalf("scope=%+v err=%v", restored.Scope, err)
	}
	restored.PrepareContext = func(context.Context, tool.Scope) (PreparedContext, error) {
		return PreparedContext{Values: map[string]string{"memory_generation": "99"}}, nil
	}
	prepared, err := (&Runtime{}).prepareContext(context.Background(), restored)
	if err != nil || prepared.Scope.Values["memory_generation"] != "3" {
		t.Fatal("replay refreshed reset generation")
	}
}
