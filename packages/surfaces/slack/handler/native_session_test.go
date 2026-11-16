package slackhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	gateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
)

type nativeTransport func(*http.Request) (*http.Response, error)

func (f nativeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nativeGrant struct{}

func (nativeGrant) Get(_ context.Context, team, channel string) (taskspace.Space, error) {
	if team != "T1" || channel != "CSPACE" {
		return taskspace.Space{}, taskspace.ErrNotFound
	}
	return taskspace.Space{TeamID: team, Channel: channel, OwnerID: "U1", OriginChannel: "CORIGIN", OriginMessageTS: "1"}, nil
}
func (nativeGrant) Save(context.Context, taskspace.Space) error { return nil }

func nativeHandler() *Handler {
	client := slack.NewTestClient(nativeTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"ok":true,"channel":{"id":"CSPACE","properties":{"record_channel":{"record_type":"agent_channel"},"agent_session":{"encoded_agent_bot_user_ids":["BOT"]}}}}`
		if req.URL.Path != "/api/conversations.info" {
			body = `{"ok":true,"ts":"denial"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}))
	access := safety.NewAccessPolicy([]string{"U1", "U2"}, []string{"CORIGIN"})
	return &Handler{Cfg: config.Config{Slack: config.SlackConfig{BotUserID: "BOT"}}, Slack: client, Conv: &triggerConversation{}, Access: access, Spaces: &taskspace.Service{Store: nativeGrant{}, Slack: client, Access: access, BotUserID: "BOT"}}
}

func TestNativeMentionAndStopUseOneWholeChannelTarget(t *testing.T) {
	h := nativeHandler()
	conv := h.Conv.(*triggerConversation)
	event := slack.Event{TeamID: "T1", Type: "app_mention", User: "U1", Channel: "CSPACE", TS: "2.0", ThreadTS: "nested", Text: "<@BOT> continue"}
	if err := h.Handle(t.Context(), "mention", event); err != nil {
		t.Fatal(err)
	}
	if len(conv.requests) != 1 || conv.requests[0].SessionKind != conversation.CodeChannelSession || conv.requests[0].ThreadTS != "" {
		t.Fatalf("native prompt lost target: %+v", conv.requests)
	}
	stop := slack.Event{TeamID: "T1", Type: "agent_session_stopped", User: "U1", Channel: "CSPACE", EventTS: "3.0"}
	if err := h.Handle(t.Context(), "stop", stop); err != nil {
		t.Fatal(err)
	}
	if len(conv.stops) != 1 || conv.stops[0].Target() != conv.requests[0].Target() {
		t.Fatalf("Stop and prompt target diverged: %+v", conv.stops)
	}
	event.Type = "message"
	event.Text = "continue without mention"
	if err := h.Handle(t.Context(), "passive", event); err != nil {
		t.Fatal(err)
	}
	event.Type = "app_mention"
	event.User = "U2"
	event.Text = "<@BOT> continue"
	if err := h.Handle(t.Context(), "foreign-owner", event); err != nil {
		t.Fatal(err)
	}
	if len(conv.requests) != 1 {
		t.Fatal("passive or foreign-owner message triggered native agent")
	}
}

func TestNativeApprovalRetainsRequesterAndWholeChannelScope(t *testing.T) {
	h := nativeHandler()
	inputs := &dedupeInputStore{}
	h.Inputs = inputs
	target := conversation.Target{Kind: conversation.CodeChannelSession, Channel: "CSPACE"}
	h.Approvals = approvalAuthorityFunc(func(_ context.Context, id string, resolution agentruntime.ApprovalResolution) (tool.Scope, error) {
		if id != target.SessionID() || resolution.UserID != "U1" {
			return tool.Scope{}, fmt.Errorf("wrong owner")
		}
		return tool.Scope{UserID: "U1", Values: map[string]string{"team_id": "T1", "channel": "CSPACE", "session_kind": string(conversation.CodeChannelSession)}}, nil
	})
	value, _ := json.Marshal(map[string]string{"session_id": target.SessionID(), "turn_id": "turn", "tool_call_id": "call"})
	interaction := gateway.Interaction{TeamID: "T1", UserID: "U1", Channel: "CSPACE", ThreadTS: "card-root", MessageTS: "card-root"}
	h.resolveApproval(t.Context(), interaction, "agent_approval_approve", string(value))
	if len(inputs.items) != 1 {
		t.Fatal("native approval not admitted")
	}
	var request conversation.Request
	if err := json.Unmarshal(inputs.items[0].Payload, &request); err != nil {
		t.Fatal(err)
	}
	if request.Target() != target || request.UserID != "U1" {
		t.Fatalf("native approval changed owner/target: %+v", request)
	}
	interaction.UserID = "U2"
	h.resolveApproval(t.Context(), interaction, "agent_approval_approve", string(value))
	if len(inputs.items) != 1 {
		t.Fatal("other participant could approve")
	}
}

func (nativeGrant) List(context.Context, string, string, int) ([]taskspace.Space, error) {
	return nil, nil
}
