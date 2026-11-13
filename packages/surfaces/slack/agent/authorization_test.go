package slackagent

import (
	"context"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	"testing"
)

func TestClaimedInputRechecksAuthorizationBeforeExecutionOrDelivery(t *testing.T) {
	catalog, _ := tool.NewCatalog()
	store := transcript.NewMemoryStore()
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test"}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	service := New(hosted.Agent{Runtime: runner}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	inputs := &memoryInputs{}
	service.Inputs = inputs
	request := conversation.Request{SessionKind: conversation.CodeChannelSession, EventID: "accepted-before-restart", UserID: "revoked", TeamID: "T1", Channel: "CSPACE", Text: "continue"}
	id := request.Target().SessionID()
	if err := service.persistInput(t.Context(), id, sessioninput.KindQueue, request); err != nil {
		t.Fatal(err)
	}
	checked := false
	service.AuthorizeInput = func(_ context.Context, got conversation.Request) (bool, error) {
		checked = true
		if got.Target() != request.Target() || got.UserID != request.UserID {
			t.Fatal("claim changed its principal")
		}
		return false, nil
	}
	service.BeforeRun = func(context.Context, string) error { t.Fatal("tool discovery ran before authorization"); return nil }
	claimed, err := service.claimNextQueue(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.run(t.Context(), id, *claimed); err != nil {
		t.Fatal(err)
	}
	if !checked || !inputs.acked[request.EventID] || client.request != nil || len(messenger.posts) != 0 || len(messenger.statuses) != 0 {
		t.Fatal("revoked input executed or retained for infinite retries")
	}
	events, err := store.Load(t.Context(), id, 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("unauthorized execution wrote a transcript: %v %+v", err, events)
	}
}
