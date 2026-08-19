package slack

import (
	"context"
	"net/http"
	"testing"
)

func TestRequiresUserConnection(t *testing.T) {
	in := ReadTargetInput{User: "Stella", ScopeChannel: "D_BOT"}
	if !in.RequiresUserConnection() {
		t.Fatal("user lookup should require user connection")
	}
	in = ReadTargetInput{Channel: "D_OTHER", ScopeChannel: "D_BOT"}
	if !in.RequiresUserConnection() {
		t.Fatal("foreign channel should require user connection")
	}
	in = ReadTargetInput{Channel: "D_BOT", ScopeChannel: "D_BOT"}
	if in.RequiresUserConnection() {
		t.Fatal("current channel may inherit scope defaults without an explicit foreign target")
	}
}

func TestReadConversationUsesHistoryForFlatDM(t *testing.T) {
	called := ""
	client := testResolveClient(t, func(r *http.Request) (*http.Response, error) {
		called = r.URL.Path
		return jsonResp(`{"ok":true,"messages":[{"user":"U1","text":"older","ts":"100.000"}]}`), nil
	})
	messages, err := client.ReadConversation(context.Background(), ReadTarget{
		Channel:  "D1",
		ThreadTS: "200.000",
		LatestTS: "200.000",
	}, 10)
	if err != nil || called != "/api/conversations.history" || len(messages) != 1 || messages[0].Text != "older" {
		t.Fatalf("messages=%#v path=%q err=%v", messages, called, err)
	}
}
