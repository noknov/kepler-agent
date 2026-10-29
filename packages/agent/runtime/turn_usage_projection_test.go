package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestTurnUsageIncludesCompletedEmptyRecoveryRequests(t *testing.T) {
	store := transcript.NewMemoryStore()
	client := &scriptedModel{responses: []model.Response{
		{FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 5, OutputTokens: 2}},
		{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 7, OutputTokens: 3}},
	}}
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Model: "test", MaxEmptyResponseRetries: 1, RetryBaseDelay: time.Nanosecond}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	request := TurnRequest{SessionID: "usage", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "inspect")}
	result, err := runner.RunTurn(t.Context(), request)
	want := model.Usage{Reported: true, InputTokens: 12, OutputTokens: 5}
	if err != nil || result.Usage != want || len(client.requests) != 2 {
		t.Fatalf("live usage=%+v requests=%d err=%v", result.Usage, len(client.requests), err)
	}
	events, err := store.Load(t.Context(), request.SessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != transcript.TurnCompleted {
			continue
		}
		var final struct {
			Usage model.Usage `json:"usage"`
		}
		if err := json.Unmarshal(event.Metadata, &final); err != nil || final.Usage != want {
			t.Fatalf("terminal usage=%+v err=%v", final.Usage, err)
		}
	}
	replay, err := runner.RunTurn(t.Context(), request)
	if err != nil || replay.Usage != want || len(client.requests) != 2 {
		t.Fatalf("replayed usage=%+v requests=%d err=%v", replay.Usage, len(client.requests), err)
	}
}

func TestFailedTurnRetainsUsageBeforeEmptyRecoveryFails(t *testing.T) {
	store := transcript.NewMemoryStore()
	client := &scriptedModel{responses: []model.Response{{FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 5, OutputTokens: 2}}, {}}, errors: []error{nil, &model.Error{Kind: model.ErrorContentPolicy, Message: "refused"}}}
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{Model: "test", MaxEmptyResponseRetries: 1, RetryBaseDelay: time.Nanosecond}, Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "usage-failed", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "inspect")})
	if model.ErrorKindOf(err) != model.ErrorContentPolicy || result.Usage.InputTokens != 5 || result.Usage.OutputTokens != 2 || len(client.requests) != 2 {
		t.Fatalf("usage=%+v requests=%d err=%v", result.Usage, len(client.requests), err)
	}
}
