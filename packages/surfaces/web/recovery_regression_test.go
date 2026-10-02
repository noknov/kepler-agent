package web

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
)

func TestCanceledTurnIsTerminalForQueue(t *testing.T) {
	store := transcript.NewMemoryStore()
	ctx := context.Background()
	_, err := store.Append(ctx, transcript.Event{ID: "audit_cancel", SessionID: "web_audit", TurnID: "audit_turn", Type: transcript.TurnCanceled, Status: "canceled"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewConversationService(hosted.Agent{}, nil, store, nil)
	terminal := svc.turnTerminal(ctx, "web_audit", "audit_turn")
	t.Logf("TurnCanceled terminal=%v", terminal)
	if !terminal {
		t.Fatal("canceled queue item will be released and retried instead of acknowledged")
	}
}
