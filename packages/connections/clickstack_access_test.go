package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestClickStackOAuthRefresh(t *testing.T) {
	var tokenValues url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			tokenValues = r.PostForm
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-2",
				"refresh_token": "refresh-2",
				"expires_in":    3600,
				"scope":         "clickstack:access",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oauth := newClickStackOAuth(ClickStackOAuthConfig{MCPURL: "https://example.test/clickstack"})
	oauth.httpClient = server.Client()
	oauth.tokenURL = server.URL + "/token"

	response, err := oauth.refresh(context.Background(), "refresh-1", "http://localhost/callback", "client-1")
	if err != nil {
		t.Fatalf("refresh() error = %v", err)
	}
	if response.AccessToken != "access-2" || response.RefreshToken != "refresh-2" || response.ExpiresIn != 3600 {
		t.Fatalf("refresh() = %+v", response)
	}
	if tokenValues.Get("grant_type") != "refresh_token" || tokenValues.Get("refresh_token") != "refresh-1" {
		t.Fatalf("token form = %#v", tokenValues)
	}
}

func TestClickStackAccessTokenRefreshesBeforeExpiry(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	service := &Service{
		Store: store,
		Config: Config{
			PublicBaseURL: "http://127.0.0.1:8765",
			SecretKey:     "test-secret",
			ClickStack:    ClickStackOAuthConfig{ServiceID: "svc-1"},
		},
	}
	ctx := context.Background()
	expired := clickStackTokenBundle{
		Access:      "stale-access",
		Refresh:     "refresh-1",
		ClientID:    "client-1",
		RedirectURI: "http://127.0.0.1:8765/oauth/clickstack/callback",
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
	}
	raw, err := encodeClickStackTokenBundle(expired)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertToken(ctx, LocalUserID, ProviderClickStack, raw, []string{"clickstack:access"}, "user@example.com"); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access",
			"refresh_token": "refresh-2",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	service.mutableState().clickstackOAuth = newClickStackOAuth(ClickStackOAuthConfig{ServiceID: "svc-1"})
	service.mutableState().clickstackOAuth.httpClient = server.Client()
	service.mutableState().clickstackOAuth.tokenURL = server.URL + "/token"

	token, err := service.ClickStackAccessToken(ctx, LocalUserID)
	if err != nil {
		t.Fatalf("ClickStackAccessToken() error = %v", err)
	}
	if token != "fresh-access" {
		t.Fatalf("token = %q", token)
	}
	stored, err := store.RawToken(ctx, LocalUserID, ProviderClickStack)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := parseClickStackTokenBundle(stored)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Access != "fresh-access" || bundle.Refresh != "refresh-2" {
		t.Fatalf("stored bundle = %+v", bundle)
	}
}

func TestClickStackStoredTokenUsable(t *testing.T) {
	if !clickStackStoredTokenUsable(`{"access":"token","refresh":"refresh"}`, true) {
		t.Fatal("expected oauth bundle with refresh to be usable")
	}
	if clickStackStoredTokenUsable(`{"access":"token"}`, true) {
		t.Fatal("expected oauth bundle without refresh to be unusable")
	}
	if !clickStackStoredTokenUsable("plain-api-key", false) {
		t.Fatal("expected api key token to be usable")
	}
}

func TestClickStackAccessTokenBackfillsAccount(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		Store: store,
		Config: Config{
			PublicBaseURL: "http://127.0.0.1:8765",
			SecretKey:     "test-secret",
			ClickStack:    ClickStackOAuthConfig{ServiceID: "svc-1"},
		},
	}
	ctx := context.Background()
	bundle := clickStackTokenBundle{
		Access:      testJWT(t, map[string]any{"sub": "google-oauth2|abc"}),
		Refresh:     "refresh-1",
		ClientID:    "client-1",
		RedirectURI: "http://127.0.0.1:8765/oauth/clickstack/callback",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	}
	raw, err := encodeClickStackTokenBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertToken(ctx, LocalUserID, ProviderClickStack, raw, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClickStackAccessToken(ctx, LocalUserID); err != nil {
		t.Fatalf("ClickStackAccessToken() error = %v", err)
	}
	conn, err := store.Get(ctx, LocalUserID, ProviderClickStack)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Account != "google-oauth2|abc" {
		t.Fatalf("account = %q", conn.Account)
	}
}

func TestClickStackAccessTokenInstancesKeepTokensAndEndpointsSeparate(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: store, Config: Config{PublicBaseURL: "https://example.com", SecretKey: "test-secret"}}
	ctx := context.Background()
	for _, item := range []struct {
		instance string
		label    string
		token    string
		url      string
	}{
		{"i-prod", "Production", "prod-token", "https://prod.example/mcp"},
		{"i-eu", "Europe", "eu-token", "https://eu.example/mcp"},
	} {
		if err := store.UpsertTokenInstance(ctx, LocalUserID, ProviderClickStack, item.instance, item.label, item.token, nil, item.label, map[string]string{"mcp_url": item.url}); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ instance, token, url string }{
		{"i-prod", "prod-token", "https://prod.example/mcp"},
		{"i-eu", "eu-token", "https://eu.example/mcp"},
	} {
		got, err := service.ClickStackAccessTokenInstance(ctx, LocalUserID, item.instance)
		if err != nil || got != item.token {
			t.Fatalf("ClickStackAccessTokenInstance(%q) = (%q, %v)", item.instance, got, err)
		}
		connection, err := store.GetInstance(ctx, LocalUserID, ProviderClickStack, item.instance)
		if err != nil || connection.Metadata["mcp_url"] != item.url {
			t.Fatalf("connection %q = (%+v, %v)", item.instance, connection, err)
		}
	}
}
