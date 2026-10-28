package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/session"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
)

func TestIntegrationGatewayAdmitsOnlyRequesterApprovalInOriginalThread(t *testing.T) {
	dsn, redisURL := os.Getenv("POSTGRES_TEST_DSN"), os.Getenv("REDIS_TEST_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("isolated PostgreSQL and Redis are required")
	}
	ctx := t.Context()
	cfg := config.Config{Storage: config.StorageConfig{PostgresDSN: dsn, RedisURL: redisURL}}
	cfg.Security.AllowedUsers = []string{"requester", "owner"}
	cfg.Security.AllowedChannels = []string{"C-shared"}
	service, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	thread := fmt.Sprintf("test-approval-%d", time.Now().UnixNano())
	sessionID := session.ID("C-shared", thread)
	defer func() {
		_, _ = service.stores.PGPool.Exec(ctx, `DELETE FROM agent_session_inputs WHERE session_id=$1`, sessionID)
		_, _ = service.stores.PGPool.Exec(ctx, `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
	}()
	store := hosted.PGTranscript{Pool: service.stores.PGPool}
	for _, actor := range []string{"requester", "revoked"} {
		call := tool.Call{ID: actor, Name: "github-approve_pr", Scope: tool.Scope{UserID: actor, SessionID: sessionID, TurnID: "turn", Values: map[string]string{"conversation_type": "channel", "team_id": "team", "channel": "C-shared", "thread_ts": thread}}}
		if _, err := store.Append(ctx, transcript.Event{SessionID: sessionID, TurnID: "turn", Type: transcript.ApprovalRequested, ToolCall: &call}); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(actor, callID, team, channel string) {
		value, _ := json.Marshal(map[string]string{"session_id": sessionID, "turn_id": "turn", "tool_call_id": callID})
		service.gateway.OnInteraction(ctx, slackgateway.Interaction{Type: "block_actions", TeamID: team, UserID: actor, Channel: channel, ThreadTS: thread, Actions: []slackgateway.InteractionAction{{ActionID: "agent_approval_approve", Value: string(value)}}})
	}
	invoke("owner", "requester", "team", "C-shared")
	invoke("revoked", "revoked", "team", "C-shared")
	invoke("requester", "requester", "foreign", "C-shared")
	invoke("requester", "requester", "team", "D-requester")
	pending, err := service.stores.Inputs.Claim(ctx, sessionID, sessioninput.KindQueue, "test-owner", time.Minute, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unauthorized decisions queued: %v %v", pending, err)
	}
	invoke("requester", "requester", "team", "C-shared")
	invoke("requester", "requester", "team", "C-shared")
	pending, err = service.stores.Inputs.Claim(ctx, sessionID, sessioninput.KindQueue, "test-owner", time.Minute, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("requester confirmation queued %d items, want 1: %v", len(pending), err)
	}
	var req conversation.Request
	if err := json.Unmarshal(pending[0].Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.ConversationType != "channel" || req.UserID != "requester" || req.Channel != "C-shared" || req.ThreadTS != thread || req.Approval == nil || !req.Approval.Approved {
		t.Fatalf("lost requester or origin: %+v", req)
	}
}
