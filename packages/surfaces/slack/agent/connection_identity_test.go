package slackagent

import (
	"encoding/json"
	"testing"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/session"
)

func TestOAuthResumeRetainsAuthenticatedWorkspaceAndConversationType(t *testing.T) {
	for _, kind := range []string{"channel", "im", "mpim"} {
		t.Run(kind, func(t *testing.T) {
			store := transcript.NewMemoryStore()
			sessionID := session.ID("C1", "thread")
			metadata, _ := json.Marshal(map[string]any{"user_id": "requester", "scope": map[string]string{"team_id": "T-origin", "channel": "C1", "thread_ts": "thread", "conversation_type": kind}})
			for _, event := range []transcript.Event{
				{SessionID: sessionID, TurnID: "original", Type: transcript.TurnStarted, Metadata: metadata},
				{SessionID: sessionID, TurnID: "original", Type: transcript.TurnCompleted, Status: string(agentruntime.TerminationPendingInput)},
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
			service.resumeAfterConnection(t.Context(), connections.Continuation{UserID: "requester", Provider: "github", SessionID: sessionID, Channel: "C1", ThreadTS: "thread"})
			state, ok, err := runner.LatestSessionState(t.Context(), sessionID)
			if err != nil || !ok || state.TurnID == "original" || state.UserID != "requester" || state.Scope["team_id"] != "T-origin" || state.Scope["conversation_type"] != kind {
				t.Fatalf("OAuth resume lost origin: %+v ok=%v err=%v", state, ok, err)
			}
		})
	}
}
