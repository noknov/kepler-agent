package slackhandler

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/memory"
	"github.com/noknov/kepler-agent/packages/safety"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	slackhome "github.com/noknov/kepler-agent/packages/surfaces/slack/home"
)

type memoryResetRecorder struct {
	identity memory.Identity
	calls    int
}

func (s *memoryResetRecorder) Reset(_ context.Context, identity memory.Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	s.identity = identity
	s.calls++
	return nil
}

func TestResetMemoryUsesSignedActorAndWorkspace(t *testing.T) {
	store := &memoryResetRecorder{}
	h := Handler{Access: safety.NewAccessPolicy([]string{"U1"}, nil), Home: slackhome.Controller{Memory: store}}
	interaction := slackgateway.Interaction{Type: "block_actions", TeamID: "T1", UserID: "U1", TriggerID: "signed-trigger", Actions: []slackgateway.InteractionAction{{ActionID: "reset_user_memory", Value: `{"user_id":"victim","team_id":"other"}`}}}
	h.HandleInteraction(context.Background(), interaction)
	if store.calls != 1 || store.identity != (memory.Identity{TeamID: "T1", UserID: "U1"}) {
		t.Fatalf("reset owner=%+v calls=%d", store.identity, store.calls)
	}
	interaction.UserID = "U2"
	h.HandleInteraction(context.Background(), interaction)
	if store.calls != 2 || store.identity.UserID != "U2" {
		t.Fatal("signed actor could not clear their own memory")
	}
	interaction.UserID = "U1"
	interaction.TeamID = ""
	h.HandleInteraction(context.Background(), interaction)
	if store.calls != 2 {
		t.Fatal("missing workspace was allowed")
	}
}
