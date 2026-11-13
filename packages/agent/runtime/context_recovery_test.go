package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func TestContextRecoveryPreservesInputThatFitsTheConfiguredWindow(t *testing.T) {
	store := transcript.NewMemoryStore()
	_, err := store.Append(t.Context(), transcript.Event{SessionID: "session", Type: transcript.UserInput, Message: messagePtr(model.TextMessage(model.RoleUser, strings.Repeat("prior facts ", 100)))})
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedModel{
		errors:    []error{&model.Error{Kind: model.ErrorContextLimit, Message: "provider context limit"}, nil},
		responses: []model.Response{{}, {Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}},
	}
	catalog, _ := tool.NewCatalog()
	compactor := &recordingCompactor{}
	runner, err := New(Config{Context: ContextConfig{MaxTokens: 12000, ReserveTokens: 5000, OutputTokens: 1000}}, Dependencies{Model: client, Tools: catalog, Transcript: store, Compactor: compactor})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("current task ", 800)
	result, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "session", TurnID: "turn", Input: model.TextMessage(model.RoleUser, input)})
	if err != nil || result.Termination != TerminationCompleted || len(client.requests) != 2 {
		t.Fatalf("recovery failed: termination=%s calls=%d err=%v", result.Termination, len(client.requests), err)
	}
	found := false
	for _, message := range client.requests[1].Messages {
		found = found || message.Text() == input
	}
	if !found || !compactor.called {
		t.Fatal("current input was removed or removable history was not compacted")
	}
}

func TestContextRecoveryReportsTheProviderAndCheckpointFailure(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: transcript.NewMemoryStore(), Compactor: &recordingCompactor{}})
	if err != nil {
		t.Fatal(err)
	}
	original := &model.Error{Kind: model.ErrorContextLimit, Message: "provider rejected request"}
	_, err = runner.recoverGenerateAfterContextLimit(context.Background(), TurnRequest{SessionID: "empty"}, model.Message{}, original)
	if !errors.Is(err, original) || !strings.Contains(err.Error(), "current input or latest tool exchange") {
		t.Fatalf("recovery hid its cause: %v", err)
	}
}

type fixedCheckpointCompactor struct{ message model.Message }

func (c fixedCheckpointCompactor) Compact(context.Context, []model.Message, int) (model.Message, error) {
	return c.message, nil
}

func TestInvalidCheckpointDoesNotReplaceCanonicalHistory(t *testing.T) {
	for _, checkpoint := range []model.Message{{}, model.TextMessage(model.RoleUser, strings.Repeat("oversized ", 10000))} {
		store := transcript.NewMemoryStore()
		catalog, _ := tool.NewCatalog()
		runner, err := New(Config{Context: ContextConfig{MaxTokens: 12000, ReserveTokens: 1000}}, Dependencies{Model: &scriptedModel{}, Tools: catalog, Transcript: store, Compactor: fixedCheckpointCompactor{checkpoint}})
		if err != nil {
			t.Fatal(err)
		}
		err = runner.compactContext(t.Context(), TurnRequest{SessionID: "session", TurnID: "turn"}, Projection{
			EstimatedTokens: 100, CoversThrough: 1, Dropped: []model.Message{model.TextMessage(model.RoleUser, "evidence")},
		}, false)
		if err == nil {
			t.Fatal("invalid checkpoint was accepted")
		}
		events, err := store.Load(t.Context(), "session", 0)
		if err != nil || len(events) != 0 {
			t.Fatalf("failed checkpoint changed canonical history: events=%d err=%v", len(events), err)
		}
	}
}
