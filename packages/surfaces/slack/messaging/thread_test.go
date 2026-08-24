package messaging

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

func TestThreadLoaderUsesBotClient(t *testing.T) {
	bot := slack.NewClient("xoxb-bot", "B1")
	loader := ThreadLoader{Bot: bot}
	if loader.Bot == nil {
		t.Fatal("expected bot client")
	}
}

func TestThreadLoaderSkipsHistoryForNewRootMessage(t *testing.T) {
	loader := ThreadLoader{Bot: slack.NewTestClient(nil)}
	history, err := loader.Load(context.Background(), slackconversation.Request{
		Channel: "D1", ThreadTS: "200.000", MessageTS: "200.000",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history = %#v, want no context for a new root message", history)
	}
}
