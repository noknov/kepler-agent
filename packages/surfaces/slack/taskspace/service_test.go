package taskspace

import (
	"context"
	"errors"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type memoryStore struct {
	space   Space
	saveErr error
}

func (m *memoryStore) Get(_ context.Context, team, channel string) (Space, error) {
	if m.space.TeamID != team || m.space.Channel != channel {
		return Space{}, ErrNotFound
	}
	return m.space, nil
}
func (m *memoryStore) Save(_ context.Context, s Space) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.space = s
	return nil
}

func (m *memoryStore) List(_ context.Context, team, user string, _ int) ([]Space, error) {
	if m.space.TeamID == team && m.space.OwnerID == user {
		return []Space{m.space}, nil
	}
	return nil, nil
}

type fakeClient struct {
	info      slack.CodeChannelInfo
	requests  []slack.CodeChannelCreate
	posted    int
	infoCalls int
}

func (c *fakeClient) CodeChannelInfo(context.Context, string) (slack.CodeChannelInfo, error) {
	c.infoCalls++
	return c.info, nil
}
func (c *fakeClient) CreateCodeChannel(_ context.Context, r slack.CodeChannelCreate) (string, error) {
	c.requests = append(c.requests, r)
	return "CSPACE", nil
}
func (*fakeClient) SetCodeContext(context.Context, string, []slack.CodeContextItem) error { return nil }
func (*fakeClient) SetCodeView(context.Context, slack.CodeView) error                     { return nil }
func (c *fakeClient) PostMarkdownMessageWithID(_ context.Context, channel, thread, text, id string) (string, error) {
	c.posted++
	return "1.0", nil
}
func nativeClient() *fakeClient {
	c := &fakeClient{info: slack.CodeChannelInfo{ID: "CSPACE"}}
	c.info.Properties.RecordChannel.RecordType = "agent_channel"
	c.info.Properties.AgentSession.Agents = []string{"BOT"}
	return c
}
func testScope() tool.Scope {
	return tool.Scope{UserID: "owner", Values: map[string]string{"surface": "slack", "team_id": "T1", "channel": "CORIGIN", "message_ts": "10.1"}}
}

func TestCreationKeepsAuthenticatedOriginAndCanRepairStoreFailure(t *testing.T) {
	client := nativeClient()
	store := &memoryStore{saveErr: errors.New("storage unavailable")}
	service := Service{Store: store, Slack: client, BotUserID: "BOT", Access: safety.NewAccessPolicy([]string{"owner"}, []string{"CORIGIN"})}
	if _, err := service.Create(t.Context(), testScope(), "task", "Goal and constraints"); err == nil {
		t.Fatal("creation hid grant persistence failure")
	}
	if client.posted != 0 {
		t.Fatal("brief posted before authorization was durable")
	}
	if _, ok, err := service.Resolve(t.Context(), "T1", "owner", "CSPACE"); err != nil || ok {
		t.Fatal("failed save granted channel access")
	}
	store.saveErr = nil
	space, err := service.Create(t.Context(), testScope(), "task", "Goal and constraints")
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || client.requests[0].SessionID != client.requests[1].SessionID || client.requests[1].OriginChannel != "CORIGIN" || client.requests[1].OriginMessageTS != "10.1" || space.OwnerID != "owner" {
		t.Fatalf("unstable origin/idempotency: %+v", client.requests)
	}
	if _, ok, err := service.Resolve(t.Context(), "T1", "owner", "CSPACE"); err != nil || !ok {
		t.Fatalf("saved owner grant unavailable: %v", err)
	}
}

func TestSpaceGrantCannotBroadenUserWorkspaceOrOriginAccess(t *testing.T) {
	client := nativeClient()
	store := &memoryStore{space: Space{TeamID: "T1", Channel: "CSPACE", OwnerID: "owner", OriginChannel: "CORIGIN", OriginMessageTS: "1"}}
	service := Service{Store: store, Slack: client, BotUserID: "BOT", Access: safety.NewAccessPolicy([]string{"owner", "other"}, []string{"CORIGIN"})}
	for _, request := range []struct{ team, user string }{{"T2", "owner"}, {"T1", "other"}, {"T1", ""}, {"T1", "outsider"}} {
		if _, ok, err := service.Resolve(t.Context(), request.team, request.user, "CSPACE"); err != nil || ok {
			t.Fatalf("foreign actor granted: %+v %v", request, err)
		}
	}
	if client.infoCalls != 0 {
		t.Fatal("unauthorized actors reached Slack metadata")
	}
	// Even unrestricted channel policy cannot turn an owned space into a shared
	// private-history session simply because another allowed user joins it.
	service.Access = safety.NewAccessPolicy([]string{"owner", "other"}, nil)
	target, ok, err := service.Resolve(t.Context(), "T1", "other", "CSPACE")
	if err != nil || ok || target.Kind != conversation.CodeChannelSession {
		t.Fatalf("owner denial lost native identity: %+v %v", target, err)
	}
	service.Access = safety.NewAccessPolicy([]string{"owner"}, []string{"CANOTHER"})
	if _, ok, err := service.Resolve(t.Context(), "T1", "owner", "CSPACE"); err != nil || ok {
		t.Fatal("revoked origin still grants access")
	}
}

func TestClassificationRequiresNativeRecordAndAssignedAgent(t *testing.T) {
	client := nativeClient()
	service := Service{Slack: client, BotUserID: "BOT"}
	target, ok, err := service.Resolve(t.Context(), "T1", "owner", "CSPACE")
	if err != nil || !ok || target.ThreadTS != "" {
		t.Fatalf("empty native properties not recognized: %+v %v", target, err)
	}
	client.info.Properties.RecordChannel.RecordType = ""
	if _, ok, _ := service.Resolve(t.Context(), "T1", "owner", "CSPACE"); ok {
		t.Fatal("name or empty code properties treated as native identity")
	}
	client.info.Properties.RecordChannel.RecordType = "agent_channel"
	client.info.Properties.AgentSession.Agents = []string{"OTHER"}
	target, ok, err = service.Resolve(t.Context(), "T1", "owner", "CSPACE")
	if err != nil || ok || target.Kind != conversation.CodeChannelSession {
		t.Fatal("foreign native session could fall through to a thread")
	}
}
