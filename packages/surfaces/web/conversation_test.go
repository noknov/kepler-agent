package web

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
)

type capturingInputStore struct {
	mu    sync.Mutex
	items []sessioninput.Item
}

func (s *capturingInputStore) Enqueue(_ context.Context, item sessioninput.Item) error {
	s.mu.Lock()
	s.items = append(s.items, item)
	s.mu.Unlock()
	return nil
}
func (*capturingInputStore) Claim(context.Context, string, sessioninput.Kind, string, time.Duration, int) ([]sessioninput.Item, error) {
	return nil, nil
}
func (*capturingInputStore) Ack(context.Context, string, string) error     { return nil }
func (*capturingInputStore) Release(context.Context, string, string) error { return nil }
func (*capturingInputStore) PendingSessions(context.Context, sessioninput.Kind, int) ([]string, error) {
	return nil, nil
}
func (*capturingInputStore) PromoteSteering(context.Context, string) (int64, error) {
	return 0, nil
}
func (*capturingInputStore) PromoteExpiredSteering(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func TestConversationListSetsHasMessages(t *testing.T) {
	store := newMemoryWebStore()
	transcriptStore := transcript.NewMemoryStore()
	svc := NewConversationService(hosted.Agent{}, store, transcriptStore, NewEventHub(safety.Redactor{}))
	owner := Identity{Provider: "slack", TenantID: "T1", SubjectID: "U1"}
	ctx := context.Background()

	empty, err := svc.Create(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	active, err := svc.Create(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transcriptStore.Append(ctx, transcript.Event{
		ID: "e1", SessionID: active.ID, Type: transcript.UserInput,
	}); err != nil {
		t.Fatal(err)
	}

	conversations, err := svc.List(ctx, owner, false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Conversation, len(conversations))
	for _, conversation := range conversations {
		byID[conversation.ID] = conversation
	}
	if byID[empty.ID].HasMessages {
		t.Fatalf("empty conversation should not have messages: %#v", byID[empty.ID])
	}
	if !byID[active.ID].HasMessages {
		t.Fatalf("active conversation should have messages: %#v", byID[active.ID])
	}
}

func TestStartTurnPersistsToWebSpecificQueueBeforeExecution(t *testing.T) {
	store := newMemoryWebStore()
	inputs := &capturingInputStore{}
	svc := NewConversationService(hosted.Agent{}, store, transcript.NewMemoryStore(), NewEventHub(safety.Redactor{}))
	svc.Inputs = inputs
	owner := Identity{Provider: "slack", TenantID: "T1", SubjectID: "U1"}
	conversation, err := svc.Create(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := svc.StartTurn(context.Background(), owner, conversation.ID, "request_1234567890", "hello")
	if err != nil {
		t.Fatal(err)
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	if len(inputs.items) != 1 || inputs.items[0].ID != turnID || inputs.items[0].Kind != sessioninput.KindWeb {
		t.Fatalf("queued items=%+v", inputs.items)
	}
}
