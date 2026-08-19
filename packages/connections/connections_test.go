package connections

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequiredErrorToolResult(t *testing.T) {
	result, err := ToolResult(&RequiredError{
		Provider: ProviderSlack,
		Title:    "Slack",
		AuthURL:  "http://localhost/oauth/slack/start?state=abc",
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
}
