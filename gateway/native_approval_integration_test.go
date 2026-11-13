package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/platform"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
)

type nativeApprovalTransport func(*http.Request) (*http.Response, error)

func (f nativeApprovalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIntegrationNativeApprovalThroughComposedGateway(t *testing.T) {
	dsn, redisURL := os.Getenv("POSTGRES_TEST_DSN"), os.Getenv("REDIS_TEST_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("isolated PostgreSQL and Redis are required")
	}
	cfg := config.Config{Storage: config.StorageConfig{PostgresDSN: dsn, RedisURL: redisURL}, Slack: config.SlackConfig{CodeChannelsEnabled: true}}
	cfg.Security.AllowedUsers = []string{"requester", "other"}
	cfg.Security.AllowedChannels = []string{"CORIGIN"}
	stores, err := platform.NewEventIngressStores(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stores.Close)
	channel := fmt.Sprintf("C%d", time.Now().UnixNano())
	client := slack.NewTestClient(nativeApprovalTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/conversations.info" {
			t.Fatalf("unexpected network operation: %s", req.URL.Path)
		}
		response := fmt.Sprintf(`{"ok":true,"channel":{"id":%q,"properties":{"record_channel":{"record_type":"agent_channel"},"agent_session":{"encoded_agent_bot_user_ids":["BOT"]}}}}`, channel)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	}))
	client.SetBotUserID("BOT")
	service, err := newWithStores(t.Context(), cfg, stores, client)
	if err != nil {
		t.Fatal(err)
	}
	target := conversation.Target{Kind: conversation.CodeChannelSession, Channel: channel}
	sessionID := target.SessionID()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = stores.PGPool.Exec(cleanup, `DELETE FROM agent_session_inputs WHERE session_id=$1`, sessionID)
		_, _ = stores.PGPool.Exec(cleanup, `DELETE FROM agent_transcript_events WHERE session_id=$1`, sessionID)
		_, _ = stores.PGPool.Exec(cleanup, `DELETE FROM slack_task_spaces WHERE channel_id=$1`, channel)
	})
	spaces := taskspace.PGStore{Pool: stores.PGPool}
	if err := spaces.Save(t.Context(), taskspace.Space{TeamID: "T1", Channel: channel, OwnerID: "requester", OriginChannel: "CORIGIN", OriginMessageTS: channel[1:] + ".0"}); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "call", Name: "slack-set_task_view", Scope: tool.Scope{UserID: "requester", SessionID: sessionID, TurnID: "turn", Values: map[string]string{"surface": "slack", "team_id": "T1", "channel": channel, "session_kind": string(conversation.CodeChannelSession)}}}
	if _, err := (hosted.PGTranscript{Pool: stores.PGPool}).Append(t.Context(), transcript.Event{ID: "native-approval-" + channel, SessionID: sessionID, TurnID: "turn", Type: transcript.ApprovalRequested, ToolCall: &call}); err != nil {
		t.Fatal(err)
	}
	token, _ := json.Marshal(map[string]string{"session_id": sessionID, "turn_id": "turn", "tool_call_id": "call"})
	invoke := func(user, team string) {
		service.gateway.OnInteraction(t.Context(), slackgateway.Interaction{Type: "block_actions", UserID: user, TeamID: team, Channel: channel, Actions: []slackgateway.InteractionAction{{ActionID: "agent_approval_approve", Value: string(token)}}})
	}
	invoke("other", "T1")
	invoke("requester", "foreign")
	denied, err := stores.Inputs.Claim(t.Context(), sessionID, sessioninput.KindQueue, "fixture", time.Minute, 10)
	if err != nil || len(denied) != 0 {
		t.Fatalf("foreign confirmation admitted: %+v %v", denied, err)
	}
	invoke("requester", "T1")
	invoke("requester", "T1")
	items, err := stores.Inputs.Claim(t.Context(), sessionID, sessioninput.KindQueue, "fixture", time.Minute, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("native requester confirmation admitted %d inputs, want one: %v", len(items), err)
	}
	var request conversation.Request
	if err := json.Unmarshal(items[0].Payload, &request); err != nil {
		t.Fatal(err)
	}
	if request.Target() != target || request.UserID != "requester" || request.Approval == nil {
		t.Fatalf("native target/actor lost: %+v", request)
	}
}
