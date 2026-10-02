package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type failInputOnceStore struct {
	transcript.Store
	failed            bool
	commitBeforeError bool
}

func (s *failInputOnceStore) Append(ctx context.Context, e transcript.Event) (transcript.Event, error) {
	if e.Type == transcript.UserInput && !s.failed {
		s.failed = true
		if s.commitBeforeError {
			if _, err := s.Store.Append(ctx, e); err != nil {
				return transcript.Event{}, err
			}
		}
		return transcript.Event{}, errors.New("synthetic transient input append failure")
	}
	return s.Store.Append(ctx, e)
}
func TestRetryRestoresInputAfterPartialStart(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "before_commit"
		if commit {
			name = "after_commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := &failInputOnceStore{Store: transcript.NewMemoryStore(), commitBeforeError: commit}
			client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done")}}}
			catalog, _ := tool.NewCatalog()
			runtime, err := New(Config{}, Dependencies{Model: client, Tools: catalog, Transcript: store})
			if err != nil {
				t.Fatal(err)
			}
			request := TurnRequest{SessionID: "session", TurnID: "turn", Input: model.TextMessage(model.RoleUser, "original request")}
			if _, err := runtime.RunTurn(ctx, request); err == nil {
				t.Fatal("expected injected input failure")
			}
			if _, err := runtime.RunTurn(ctx, request); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.RunTurn(ctx, request); err != nil {
				t.Fatal(err)
			} // completed replay
			if len(client.requests) != 1 {
				t.Fatalf("model called %d times", len(client.requests))
			}
			found := false
			for _, message := range client.requests[0].Messages {
				found = found || message.Text() == "original request"
			}
			if !found {
				t.Fatal("retry reached model without original input")
			}
			events, err := store.Load(ctx, "session", 0)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.Type == transcript.UserInput {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("durable inputs = %d", count)
			}
		})
	}
}
func TestRejectImpossibleContextBudget(t *testing.T) {
	input := model.TextMessage(model.RoleUser, strings.Repeat("a", 160))
	p := NewBoundedProjector(ContextConfig{MaxTokens: 100, ReserveTokens: 60, OutputTokens: 60})
	result, err := p.Project(context.Background(), ProjectRequest{Events: []transcript.Event{{Sequence: 1, TurnID: "t", Type: transcript.UserInput, Message: &input}}})
	t.Logf("estimated_input=%d reserve=60 output=60 window=100 err=%v", result.EstimatedTokens, err)
	if err == nil {
		t.Fatal("nonpositive input budget was reset to the full window")
	}
}

func TestRuntimeReservesConfiguredOutputBeforeCallingModel(t *testing.T) {
	client := &scriptedModel{}
	catalog, _ := tool.NewCatalog()
	runtime, err := New(Config{MaxOutputTokens: 60, Context: ContextConfig{MaxTokens: 100, ReserveTokens: 40}}, Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.RunTurn(context.Background(), TurnRequest{Input: model.TextMessage(model.RoleUser, "hi")})
	if err == nil || result.Termination != TerminationBudgetExhausted || len(client.requests) != 0 {
		t.Fatalf("termination=%s error=%v model calls=%d", result.Termination, err, len(client.requests))
	}
}

type stalledTerminalStore struct{ transcript.Store }

func (s stalledTerminalStore) Append(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if event.Type.IsTurnTerminal() {
		if _, ok := ctx.Deadline(); !ok {
			return transcript.Event{}, errors.New("missing cleanup deadline")
		}
		<-ctx.Done()
		return transcript.Event{}, ctx.Err()
	}
	return s.Store.Append(ctx, event)
}
func TestTerminalPersistenceHasBoundedCleanup(t *testing.T) {
	client := &scriptedModel{responses: []model.Response{{Message: model.TextMessage(model.RoleAssistant, "done")}}}
	catalog, _ := tool.NewCatalog()
	runtime, err := New(Config{CleanupTimeout: 20 * time.Millisecond}, Dependencies{Model: client, Tools: catalog, Transcript: stalledTerminalStore{transcript.NewMemoryStore()}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.RunTurn(context.Background(), TurnRequest{Input: model.TextMessage(model.RoleUser, "hi")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error = %v", err)
	}
}
