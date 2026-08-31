package web

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
)

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

	conversations, err := svc.List(ctx, owner, false, 10)
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
