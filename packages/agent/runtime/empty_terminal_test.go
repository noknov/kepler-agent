package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestEmptyRecoveryHonorsExplicitTerminalReceipts(t *testing.T) {
	for _, reason := range []model.FinishReason{model.FinishContent, model.FinishCanceled, "", model.FinishToolCalls} {
		t.Run(string(reason), func(t *testing.T) {
			client := &scriptedModel{responses: []model.Response{
				{Message: model.Message{Role: model.RoleAssistant}, FinishReason: reason, Usage: model.Usage{Reported: true, OutputTokens: 9}},
				{Message: model.TextMessage(model.RoleAssistant, "unexpected retry"), FinishReason: model.FinishStop},
			}}
			catalog, _ := tool.NewCatalog()
			runner, err := New(Config{Model: "test", MaxEmptyResponseRetries: 3, RetryBaseDelay: time.Nanosecond}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "terminal", Input: model.TextMessage(model.RoleUser, "inspect")})
			want := TerminationEmptyResponse
			if reason == model.FinishContent {
				want = TerminationModelError
				if model.ErrorKindOf(err) != model.ErrorContentPolicy {
					t.Errorf("refusal lost content-policy classification: %v", err)
				}
			}
			if reason == model.FinishCanceled {
				want = TerminationCanceled
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled receipt lost cancellation: %v", err)
				}
			}
			if err == nil || len(client.requests) != 1 || result.Termination != want || result.Usage.OutputTokens != 9 {
				t.Fatalf("receipt=%s result=%+v requests=%d err=%v", reason, result, len(client.requests), err)
			}
		})
	}
}
