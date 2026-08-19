package slacktool

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestPreferConnectedThreadReaderUsesBotForCurrentConversation(t *testing.T) {
	bot := slack.NewClient("xoxb-bot", "B1")
	source := PreferConnectedThreadReader{
		Connected: ConnectedThreadReader{},
		Bot:       BotThreadReader{Slack: bot},
	}
	got, err := source.Client(context.Background(), tool.Call{
		Scope: tool.Scope{Values: map[string]string{"channel": "C1", "thread_ts": "1.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != bot {
		t.Fatal("expected bot client for current conversation")
	}
}

func TestPreferConnectedThreadReaderRequiresConnectionForExternalReads(t *testing.T) {
	bot := slack.NewClient("xoxb-bot", "B1")
	source := PreferConnectedThreadReader{
		Connected: ConnectedThreadReader{},
		Bot:       BotThreadReader{Slack: bot},
	}
	_, err := source.Client(context.Background(), tool.Call{
		Arguments: []byte(`{"user":"UOTHER"}`),
		Scope:     tool.Scope{Values: map[string]string{"channel": "C1"}},
	})
	if err == nil {
		t.Fatal("expected error when external read requires user connection")
	}
}
