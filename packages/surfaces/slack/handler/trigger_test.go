package slackhandler

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type triggerConversation struct {
	requests []conversation.Request
	stops    []conversation.StopRequest
}

func (c *triggerConversation) StopSession(_ context.Context, req conversation.StopRequest) error {
	c.stops = append(c.stops, req)
	return nil
}

func TestNativeStopRequiresAccessButNotMention(t *testing.T) {
	for _, channel := range []string{"C1", "D1", "C2"} {
		for _, user := range []string{"U1", "U2"} {
			conv := &triggerConversation{}
			h := Handler{Conv: conv, Access: safety.NewAccessPolicy([]string{"U1"}, []string{"C1"})}
			err := h.Handle(context.Background(), "stop-event", slack.Event{Type: "agent_session_stopped", Channel: channel, ThreadTS: "1.0", User: user, EventTS: "2.0"})
			want := user == "U1" && channel != "C2"
			if err != nil || (len(conv.stops) == 1) != want || len(conv.requests) != 0 {
				t.Fatalf("channel=%s user=%s stops=%d err=%v", channel, user, len(conv.stops), err)
			}
			if want && conv.stops[0].EventTS != "2.0" {
				t.Fatal("stop lost the Slack event timestamp")
			}
		}
	}
}

func (c *triggerConversation) HandleMention(_ context.Context, req conversation.Request) (bool, error) {
	c.requests = append(c.requests, req)
	return true, nil
}

func TestDMsAndChannelMentionsTrigger(t *testing.T) {
	for _, tt := range []struct {
		name, kind, channel, text, subtype, bot, user string
		want                                          int
	}{
		{name: "channel mention", kind: "app_mention", channel: "C1", text: "<@BOT> review", want: 1},
		{name: "thread reply", kind: "message", channel: "C1", text: "continue"},
		{name: "pending answer", kind: "message", channel: "C1", text: "production"},
		{name: "mention event without bot", kind: "app_mention", channel: "C1", text: "<!subteam^GROUP> review"},
		{name: "another user mention", kind: "app_mention", channel: "C1", text: "<@OTHER> review"},
		{name: "display name is not a mention", kind: "message", channel: "C1", text: "斗包 帮忙"},
		{name: "duplicate channel message", kind: "message", channel: "C1", text: "<@BOT> review"},
		{name: "DM unmentioned", kind: "message", channel: "D1", text: "review", want: 1},
		{name: "DM mentioned", kind: "message", channel: "D1", text: "<@BOT> review", want: 1},
		{name: "DM duplicate mention event", kind: "app_mention", channel: "D1", text: "<@BOT> review"},
		{name: "standalone file notification", kind: "file_shared", channel: "D1", text: "<@BOT> review"},
		{name: "attached file mentioned", kind: "app_mention", channel: "C1", text: "<@BOT> review", subtype: "file_share", want: 1},
		{name: "DM attached file unmentioned", kind: "message", channel: "D1", text: "review", subtype: "file_share", want: 1},
		{name: "channel attached file unmentioned", kind: "message", channel: "C1", text: "review", subtype: "file_share"},
		{name: "DM bot message", kind: "message", channel: "D1", text: "review", bot: "BOTHER", want: 1},
		{name: "edited message", kind: "message", channel: "D1", text: "<@BOT> review", subtype: "message_changed"},
		{name: "own bot mention", kind: "app_mention", channel: "C1", text: "<@BOT> review", bot: "BSELF", user: "BOT"},
		{name: "bot without mention", kind: "app_mention", channel: "C1", text: "review", bot: "BOTHER"},
		{name: "bot duplicate delivery", kind: "message", channel: "C1", text: "<@BOT> review", bot: "BOTHER"},
		{name: "bot subtype", kind: "app_mention", channel: "C1", text: "<@BOT> review", subtype: "bot_message", bot: "BOTHER", want: 1},
		{name: "bot message", kind: "app_mention", channel: "C1", text: "<@BOT> review", bot: "BOTHER", want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conv := &triggerConversation{}
			cfg := config.Config{}
			cfg.Slack.BotUserID = "BOT"
			h := Handler{Cfg: cfg, Conv: conv, Access: safety.NewAccessPolicy(nil, []string{"C1"})}
			user := tt.user
			if user == "" {
				user = "U1"
			}
			err := h.Handle(context.Background(), "event", slack.Event{Type: tt.kind, Channel: tt.channel, User: user, BotID: tt.bot, Text: tt.text, Subtype: tt.subtype, TS: "2.0", ThreadTS: "1.0"})
			if err != nil || len(conv.requests) != tt.want {
				t.Fatalf("requests=%d err=%v", len(conv.requests), err)
			}
			if tt.want > 0 && conv.requests[0].Text != "review" {
				t.Fatalf("uncleaned input %q", conv.requests[0].Text)
			}
		})
	}
}

func TestRequestPreservesSlackConversationType(t *testing.T) {
	for _, tc := range []struct{ eventType, channel, kind, want string }{
		{"app_mention", "C1", "", "channel"},
		{"app_mention", "G1", "group", "group"},
		{"app_mention", "G1", "mpim", "mpim"},
		{"message", "D1", "im", "im"},
		{"message", "D1", "", ""},
	} {
		conv := &triggerConversation{}
		cfg := config.Config{}
		cfg.Slack.BotUserID = "BOT"
		h := Handler{Cfg: cfg, Conv: conv}
		if err := h.Handle(t.Context(), "event", slack.Event{Type: tc.eventType, Channel: tc.channel, ChannelType: tc.kind, User: "requester", Text: "<@BOT> approve", TS: "2.0", ThreadTS: "1.0"}); err != nil {
			t.Fatal(err)
		}
		if len(conv.requests) != 1 || conv.requests[0].ConversationType != tc.want {
			t.Fatalf("%+v produced %+v", tc, conv.requests)
		}
	}
}
