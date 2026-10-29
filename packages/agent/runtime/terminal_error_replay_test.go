package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestTerminalReplayPreservesErrorClassification(t *testing.T) {
	for _, original := range []error{
		fmt.Errorf("operator stopped turn: %w", context.Canceled),
		fmt.Errorf("model request timed out: %w", context.DeadlineExceeded),
		&model.Error{Kind: model.ErrorContentPolicy, Message: "private provider detail"},
		&model.Error{Kind: model.ErrorProtocol, Message: "private malformed response"},
	} {
		t.Run(original.Error(), func(t *testing.T) {
			client := &scriptedModel{errors: []error{original}}
			catalog, _ := tool.NewCatalog()
			runner, err := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			request := TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "inspect")}
			first, liveErr := runner.RunTurn(t.Context(), request)
			replay, replayErr := runner.RunTurn(t.Context(), request)
			if liveErr == nil || replayErr == nil || first.Termination != replay.Termination || liveErr.Error() != replayErr.Error() || len(client.requests) != 1 {
				t.Fatalf("first=%+v err=%v replay=%+v err=%v requests=%d", first, liveErr, replay, replayErr, len(client.requests))
			}
			if model.ErrorKindOf(liveErr) != model.ErrorKindOf(replayErr) || errors.Is(liveErr, context.Canceled) != errors.Is(replayErr, context.Canceled) || errors.Is(liveErr, context.DeadlineExceeded) != errors.Is(replayErr, context.DeadlineExceeded) {
				t.Fatalf("terminal replay changed error classification: live=%T %v replay=%T %v", liveErr, liveErr, replayErr, replayErr)
			}
		})
	}
}
