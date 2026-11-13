package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/llm/chatgpt"
)

func TestInputContextIdentityTracksAccountWithoutExposingCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	credentials := chatgpt.Session{ClientID: "client", HostID: "host", Subject: "alice", AccessToken: "private-access-value", RefreshToken: "private-refresh-value", Scope: "chatgpt.tokens.use.direct resource.invoke", ExpiresAt: time.Now().Add(time.Hour)}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(credentials)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	client := bindClient(&recordingWire{}, Config{Provider: "chatgpt", Protocol: "responses", ChatGPTCredentialsFile: path})
	alice, err := client.InputContextKey(t.Context(), "m")
	if err != nil || alice == "" {
		t.Fatalf("no context identity: %v", err)
	}
	response, err := client.Generate(t.Context(), model.Request{Model: "m"}, nil)
	if err != nil || response.InputContextKey != alice {
		t.Fatalf("measurement route differs from planned route: %v", err)
	}
	credentials.AccessToken = "rotated-access-value"
	write()
	refreshed, err := client.InputContextKey(t.Context(), "m")
	if err != nil || refreshed != alice {
		t.Fatal("ordinary credential refresh invalidated the account identity")
	}
	credentials.Subject = "bob"
	write()
	bob, err := client.InputContextKey(t.Context(), "m")
	if err != nil || bob == alice {
		t.Fatal("another account inherited the context identity")
	}
	for _, key := range []string{alice, bob} {
		for _, private := range []string{credentials.AccessToken, credentials.RefreshToken, "alice", "bob", path} {
			if strings.Contains(key, private) {
				t.Fatal("context identity disclosed private data")
			}
		}
	}
}

func TestUnknownRemoteContextIdentityDoesNotGuessTheHostedRoute(t *testing.T) {
	client := &Client{Host: newKeplerRemote("https://gateway.example", "fixture-key", time.Second)}
	key, err := client.InputContextKey(t.Context(), "m")
	if err != nil || key != "" {
		t.Fatalf("remote endpoint was mistaken for provider identity: %q %v", key, err)
	}
}
