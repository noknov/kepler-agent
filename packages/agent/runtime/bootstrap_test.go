package runtime

import (
	"context"
	"errors"
	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"testing"
)

func TestHistoryLoaderRetriesTransientFailure(t *testing.T) {
	store := transcript.NewMemoryStore()
	loads := 0
	loader := func(context.Context) ([]model.Message, error) {
		loads++
		if loads == 1 {
			return nil, errors.New("transient history failure")
		}
		return []model.Message{model.TextMessage(model.RoleUser, "keep compatibility")}, nil
	}
	runner := bootstrapRuntime(t, store)
	req := TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "continue"), HistoryLoader: loader}
	if _, err := runner.RunTurn(t.Context(), req); err == nil {
		t.Fatal("history failure ignored")
	}
	// Simulate a restart: only durable state survives.
	runner = bootstrapRuntime(t, store)
	if _, err := runner.RunTurn(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if loads != 2 {
		t.Fatalf("loads=%d", loads)
	}
	assertHistoryImportedOnce(t, store, 1)
}

func TestHistoryBatchFailureAndAmbiguousCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "lost_commit_response"}[committed], func(t *testing.T) {
			base := transcript.NewMemoryStore()
			store := &bootstrapFailureStore{MemoryStore: base, committed: committed, fail: true}
			loads := 0
			loader := func(context.Context) ([]model.Message, error) {
				loads++
				return []model.Message{model.TextMessage(model.RoleUser, "constraint"), model.TextMessage(model.RoleAssistant, "acknowledged")}, nil
			}
			runner := bootstrapRuntime(t, store)
			req := TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "continue"), HistoryLoader: loader}
			if _, err := runner.RunTurn(t.Context(), req); err == nil {
				t.Fatal("commit failure ignored")
			}
			events, _ := base.Load(t.Context(), "s", 0)
			messages := 0
			for _, event := range events {
				if event.Message != nil {
					messages++
				}
			}
			if !committed && messages != 0 {
				t.Fatal("partial history escaped failed transaction")
			}
			runner = bootstrapRuntime(t, store)
			if _, err := runner.RunTurn(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			wantLoads := 2
			if committed {
				wantLoads = 1
			}
			if loads != wantLoads {
				t.Fatalf("loader calls=%d want=%d", loads, wantLoads)
			}
			assertHistoryImportedOnce(t, base, 2)
		})
	}
}

func TestHistoryBootstrapRecoversLegacyPartialImport(t *testing.T) {
	store := transcript.NewMemoryStore()
	first := model.TextMessage(model.RoleUser, "constraint")
	store.Append(t.Context(), transcript.Event{SessionID: "s", Type: transcript.SessionStarted})
	store.Append(t.Context(), transcript.Event{SessionID: "s", Type: transcript.UserInput, Message: &first, Metadata: []byte(`{"source":"imported_history"}`)})
	runner := bootstrapRuntime(t, store)
	_, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "t", Input: model.TextMessage(model.RoleUser, "continue"), History: []model.Message{first, model.TextMessage(model.RoleAssistant, "acknowledged")}})
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryImportedOnce(t, store, 2)
}

func TestLegacyCanonicalSessionDoesNotReloadHistory(t *testing.T) {
	store := transcript.NewMemoryStore()
	store.Append(t.Context(), transcript.Event{SessionID: "s", TurnID: "old", Type: transcript.TurnStarted})
	runner := bootstrapRuntime(t, store)
	_, err := runner.RunTurn(t.Context(), TurnRequest{SessionID: "s", TurnID: "new", Input: model.TextMessage(model.RoleUser, "continue"), HistoryLoader: func(context.Context) ([]model.Message, error) {
		t.Fatal("legacy session refetched history")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func bootstrapRuntime(t *testing.T, store transcript.Store) *Runtime {
	t.Helper()
	catalog, _ := tool.NewCatalog()
	runner, err := New(Config{}, Dependencies{Model: &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop}}}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func assertHistoryImportedOnce(t *testing.T, store transcript.Store, want int) {
	t.Helper()
	events, err := store.Load(t.Context(), "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	count, complete := 0, 0
	for _, event := range events {
		if string(event.Metadata) == `{"source":"imported_history"}` {
			count++
		}
		if event.Type == transcript.SessionInitialized {
			complete++
		}
	}
	if count != want || complete != 1 {
		t.Fatalf("history=%d want=%d initialized=%d", count, want, complete)
	}
}

type bootstrapFailureStore struct {
	*transcript.MemoryStore
	committed, fail bool
}

func (s *bootstrapFailureStore) AppendBatch(ctx context.Context, events []transcript.Event) ([]transcript.Event, error) {
	if s.fail {
		s.fail = false
		if s.committed {
			if _, err := s.MemoryStore.AppendBatch(ctx, events); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("injected commit failure")
	}
	return s.MemoryStore.AppendBatch(ctx, events)
}
