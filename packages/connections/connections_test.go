package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRequiredErrorToolResult(t *testing.T) {
	result, err := ToolResult(&RequiredError{
		Provider: ProviderSlack,
		Title:    "Slack",
		AuthURL:  "http://localhost/oauth/slack/connect?user_id=U1&exp=1&sig=abc",
	})
	if err != nil {
		t.Fatalf("ToolResult() error = %v", err)
	}
	if !result.IsError || result.ErrorCode != "connection_required" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.NeedsUserInput {
		t.Fatalf("expected NeedsUserInput=true, got %+v", result)
	}
	if result.Metadata["auth_url"] == "" {
		t.Fatalf("expected auth_url metadata, got %+v", result.Metadata)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := t.TempDir() + "/connections.json"
	store, err := NewFileStore(path, "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	ctx := context.Background()
	if err := store.CreateOAuthState(ctx, LocalUserID, ProviderSlack, "state-1", time.Now().UTC().Add(time.Minute), OAuthStateMeta{}); err != nil {
		t.Fatalf("CreateOAuthState() error = %v", err)
	}
	userID, provider, _, err := store.PeekOAuthState(ctx, "state-1")
	if err != nil || userID != LocalUserID || provider != ProviderSlack {
		t.Fatalf("PeekOAuthState() = (%q, %q, %v)", userID, provider, err)
	}
	if err := store.UpsertToken(ctx, LocalUserID, ProviderSlack, "xoxp-test", []string{"files:read"}, "U123"); err != nil {
		t.Fatalf("UpsertToken() error = %v", err)
	}
	token, err := store.Token(ctx, LocalUserID, ProviderSlack)
	if err != nil || token != "xoxp-test" {
		t.Fatalf("Token() = (%q, %v)", token, err)
	}
	if _, _, _, err := store.ConsumeOAuthState(ctx, "state-1"); err != nil {
		t.Fatalf("ConsumeOAuthState() error = %v", err)
	}
	if _, _, _, err := store.PeekOAuthState(ctx, "state-1"); err == nil {
		t.Fatal("expected consumed oauth state to be invalid")
	}
}

func TestServiceRequiredIncludesAuthURL(t *testing.T) {
	path := t.TempDir() + "/connections.json"
	store, err := NewFileStore(path, "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	service := Service{
		Store: store,
		Config: Config{
			PublicBaseURL: "http://127.0.0.1:8765",
			SecretKey:     "test-secret",
			Slack: SlackOAuthConfig{
				ClientID:     "id",
				ClientSecret: "secret",
			},
		},
	}
	err = service.Required(LocalUserID, ProviderSlack)
	var required *RequiredError
	if !errors.As(err, &required) || required.AuthURL == "" {
		t.Fatalf("Required() = %v, want RequiredError with auth url", err)
	}
	if !strings.Contains(required.AuthURL, "/oauth/slack/connect?") {
		t.Fatalf("Required() auth url = %q, want /connect URL", required.AuthURL)
	}
}

func TestFileStoreKeepsMultipleProviderInstances(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertTokenInstance(ctx, "U1", ProviderClickStack, "i-prod", "Production", "prod-token", nil, "prod", map[string]string{"mcp_url": "https://prod.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTokenInstance(ctx, "U1", ProviderClickStack, "i-eu", "EU", "eu-token", nil, "eu", map[string]string{"mcp_url": "https://eu.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListInstances(ctx, "U1")
	if err != nil || len(items) != 2 {
		t.Fatalf("ListInstances() = (%+v, %v)", items, err)
	}
	if got, err := store.TokenInstance(ctx, "U1", ProviderClickStack, "i-eu"); err != nil || got != "eu-token" {
		t.Fatalf("TokenInstance() = (%q, %v)", got, err)
	}
	if items[0].Metadata["mcp_url"] == "" || items[0].InstanceID == items[1].InstanceID {
		t.Fatalf("instances lost identity or metadata: %+v", items)
	}
}

func TestNewInstanceIDIsOpaqueAndUnique(t *testing.T) {
	first, err := NewInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second == "" || first == second {
		t.Fatalf("NewInstanceID() = %q, %q", first, second)
	}
	if got := NormalizeInstanceID("EU Logs / Production"); got != "eu-logs-production" {
		t.Fatalf("NormalizeInstanceID() = %q", got)
	}
}
