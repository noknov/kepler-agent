package messaging

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestThreadLoaderClientForPrefersUserToken(t *testing.T) {
	bot := slack.NewClient("xoxb-bot", "B1")
	loader := ThreadLoader{
		Bot:       bot,
		BotUserID: "B1",
		UserToken: func(context.Context, string) (string, error) { return "xoxp-user", nil },
	}
	if got := loader.ClientFor(context.Background(), "U1"); got == bot {
		t.Fatal("expected a distinct user-scoped client")
	}
}

func TestThreadLoaderClientForFallsBackToBot(t *testing.T) {
	bot := slack.NewClient("xoxb-bot", "B1")
	loader := ThreadLoader{Bot: bot, BotUserID: "B1"}
	if got := loader.ClientFor(context.Background(), "U1"); got != bot {
		t.Fatal("expected bot client")
	}
}
