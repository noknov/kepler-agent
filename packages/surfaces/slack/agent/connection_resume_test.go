package slackagent

import (
	"context"
	"encoding/json"
	"testing"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/session"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func waitingConnectionService(t *testing.T) (*Service, ConnectionContinuation) {
	t.Helper()
	store := transcript.NewMemoryStore()
	id := session.ID("C1", "thread")
	metadata, _ := json.Marshal(map[string]any{"user_id": "requester", "scope": map[string]string{"team_id": "T-origin", "conversation_type": "channel"}})
	for _, event := range []transcript.Event{
		{SessionID: id, TurnID: "waiting-turn", Type: transcript.TurnStarted, Metadata: metadata},
		{SessionID: id, TurnID: "waiting-turn", Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingInput)},
	} {
		if _, err := store.Append(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	catalog, _ := tool.NewCatalog()
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: &replyModel{}, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	service := New(hosted.Agent{Runtime: runner}, &fakeMessenger{}, safety.PromptPolicy{}, safety.Redactor{}, nil)
	service.Inputs = &memoryInputs{}
	return service, ConnectionContinuation{UserID: "requester", Provider: "github", SessionID: id, TurnID: "waiting-turn", TeamID: "T-origin", ConversationType: "channel", Channel: "C1", ThreadTS: "thread"}
}

type cancelAwareInputs struct{ memoryInputs }

func (s *cancelAwareInputs) Enqueue(ctx context.Context, item sessioninput.Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memoryInputs.Enqueue(ctx, item)
}

func TestOAuthResumeStopsWithItsCaller(t *testing.T) {
	service, continuation := waitingConnectionService(t)
	inputs := &cancelAwareInputs{}
	service.Inputs = inputs
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	service.resumeAfterConnection(ctx, continuation)
	if len(inputs.items) != 0 {
		t.Fatal("canceled OAuth processing admitted new work")
	}
}

func TestRepeatedOAuthSignalAdmitsOneResume(t *testing.T) {
	service, continuation := waitingConnectionService(t)
	service.resumeAfterConnection(t.Context(), continuation)
	service.resumeAfterConnection(t.Context(), continuation)
	inputs := service.Inputs.(*memoryInputs)
	if len(inputs.items) != 1 {
		t.Fatalf("duplicate wake-up admitted %d inputs", len(inputs.items))
	}
	var req conversation.Request
	if err := json.Unmarshal(inputs.items[0].Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "requester" {
		t.Fatalf("lost requester: %v", req)
	}
}

func TestOAuthResumeRejectsMismatchedSession(t *testing.T) {
	service, continuation := waitingConnectionService(t)
	continuation.SessionID = "another-session"
	service.resumeAfterConnection(t.Context(), continuation)
	if len(service.Inputs.(*memoryInputs).items) != 0 {
		t.Fatal("mismatched continuation resumed a conversation")
	}
}

func TestOAuthResumeCannotWakeANewerWaitingTurn(t *testing.T) {
	service, continuation := waitingConnectionService(t)
	continuation.TurnID = "an-older-turn"
	service.resumeAfterConnection(t.Context(), continuation)
	service.startPending(t.Context(), continuation.SessionID)
	inputs := service.Inputs.(*memoryInputs)
	if len(inputs.items) != 1 || !inputs.acked[inputs.items[0].ID] || len(service.Messenger.(*fakeMessenger).posts) != 0 {
		t.Fatal("an older authorization resumed a newer waiting turn")
	}
}

func TestOAuthCompletionBeforeOriginalTerminalWaitsForOwnedResumption(t *testing.T) {
	store := transcript.NewMemoryStore()
	id := session.ID("C1", "thread")
	metadata := json.RawMessage(`{"user_id":"requester","scope":{"team_id":"T-origin","conversation_type":"channel"}}`)
	if _, err := store.Append(t.Context(), transcript.Event{SessionID: id, TurnID: "original", Type: transcript.TurnStarted, Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	catalog, _ := tool.NewCatalog()
	client := &replyModel{}
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	s := New(hosted.Agent{Runtime: runner}, messenger, safety.PromptPolicy{}, safety.Redactor{}, nil)
	inputs := &memoryInputs{}
	s.Inputs = inputs
	s.active[id] = &activeRun{userID: "requester", turnID: "original"}
	s.resumeAfterConnection(t.Context(), ConnectionContinuation{UserID: "requester", Provider: "notion", SessionID: id, TurnID: "original", TeamID: "T-origin", ConversationType: "channel", Channel: "C1", ThreadTS: "thread"})
	if len(inputs.items) != 1 || inputs.items[0].Kind != sessioninput.KindQueue || client.request != nil {
		t.Fatalf("early grant was lost or executed immediately: %+v", inputs.items)
	}
	if _, err := store.Append(t.Context(), transcript.Event{SessionID: id, TurnID: "original", Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingInput)}); err != nil {
		t.Fatal(err)
	}
	delete(s.active, id)
	s.startPending(t.Context(), id)
	if client.request == nil || !inputs.acked[inputs.items[0].ID] || len(messenger.posts) != 1 {
		t.Fatal("early grant did not resume after the original terminal boundary")
	}
}
