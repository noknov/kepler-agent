package slacktool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
)

func TestUserPostMessageRequiresConnection(t *testing.T) {
	store, err := connections.NewFileStore(filepath.Join(t.TempDir(), "connections.json"), strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	source := ConnectedClientSource{Service: connections.Service{
		Store: store,
		Config: connections.Config{
			PublicBaseURL: "https://example.com",
			SecretKey:     strings.Repeat("k", 32),
			Slack:         connections.SlackOAuthConfig{ClientID: "id", ClientSecret: "secret"},
		},
	}}
	result, err := (UserPostMessageTool{Source: source}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"channel":"C123","text":"hello"}`),
		Scope:     tool.Scope{UserID: "U123"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ErrorCode != "connection_required" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestUserPostMessageValidatesInput(t *testing.T) {
	_, err := (UserPostMessageTool{Source: ConnectedClientSource{}}).Execute(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"channel":"","text":"hello"}`),
		Scope:     tool.Scope{UserID: "U123"},
	})
	if err == nil || !strings.Contains(err.Error(), "channel is required") {
		t.Fatalf("expected channel validation error, got %v", err)
	}
}
