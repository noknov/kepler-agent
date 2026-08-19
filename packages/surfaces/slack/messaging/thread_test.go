package messaging

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestThreadLoaderUsesBotClient(t *testing.T) {
	bot := slack.NewClient("xoxb-bot", "B1")
	loader := ThreadLoader{Bot: bot}
	if loader.Bot == nil {
		t.Fatal("expected bot client")
	}
}
