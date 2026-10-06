package slackhandler

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type triggerConversation struct{ requests []conversation.Request }

func (c *triggerConversation) HandleMention(_ context.Context, req conversation.Request) (bool, error) {
	c.requests = append(c.requests, req)
	return true, nil
}

func TestDMsAndChannelMentionsTrigger(t *testing.T) {
	for _, tt := range []struct {
		name, kind, channel, text, subtype, bot string
		want                                    int
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
		{name: "DM bot message", kind: "message", channel: "D1", text: "review", bot: "BOTHER"},
		{name: "edited message", kind: "message", channel: "D1", text: "<@BOT> review", subtype: "message_changed"},
		{name: "bot message", kind: "app_mention", channel: "C1", text: "<@BOT> review", bot: "BOTHER"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conv := &triggerConversation{}
			cfg := config.Config{}
			cfg.Slack.BotUserID = "BOT"
			h := Handler{Cfg: cfg, Conv: conv, Access: safety.NewAccessPolicy(nil, []string{"C1"})}
			err := h.Handle(context.Background(), "event", slack.Event{Type: tt.kind, Channel: tt.channel, User: "U1", BotID: tt.bot, Text: tt.text, Subtype: tt.subtype, TS: "2.0", ThreadTS: "1.0"})
			if err != nil || len(conv.requests) != tt.want {
				t.Fatalf("requests=%d err=%v", len(conv.requests), err)
			}
			if tt.want > 0 && conv.requests[0].Text != "review" {
				t.Fatalf("uncleaned input %q", conv.requests[0].Text)
			}
		})
	}
}
