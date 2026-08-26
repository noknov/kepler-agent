package slacktool

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
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

func TestPreferConnectedThreadReaderUsesUserForExternalReads(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertToken(ctx, "U1", connections.ProviderSlack, "xoxp-user", []string{"files:read"}, "U1"); err != nil {
		t.Fatal(err)
	}
	bot := slack.NewClient("xoxb-bot", "B1")
	source := PreferConnectedThreadReader{
		Connected: ConnectedThreadReader{Service: connections.Service{Store: store}},
		Bot:       BotThreadReader{Slack: bot},
	}
	got, err := source.Client(ctx, tool.Call{
		Arguments: []byte(`{"link":"https://wati-io.slack.com/archives/COTHER/p123"}`),
		Scope:     tool.Scope{UserID: "U1", Values: map[string]string{"channel": "C1", "thread_ts": "1.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got == bot {
		t.Fatal("expected connected user client for external read")
	}
}
