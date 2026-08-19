package slacktool

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/connections"
)

func TestConnectedThreadReaderUsesStoredToken(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertToken(ctx, "U1", connections.ProviderSlack, "xoxp-user", []string{"files:read"}, "U1"); err != nil {
		t.Fatal(err)
	}
	source := ConnectedThreadReader{Service: connections.Service{Store: store}}
	got, err := source.Client(ctx, tool.Call{
		Scope: tool.Scope{UserID: "U1", Values: map[string]string{"channel": "C1", "thread_ts": "1.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected connected user client")
	}
}

func TestConnectedThreadReaderRequiresConnection(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	source := ConnectedThreadReader{Service: connections.Service{Store: store}}
	_, err = source.Client(context.Background(), tool.Call{
		Scope: tool.Scope{UserID: "U1", Values: map[string]string{"channel": "C1", "thread_ts": "1.0"}},
	})
	if err == nil {
		t.Fatal("expected error when user is not connected")
	}
}
