package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNotionOAuthAuthorizeURL(t *testing.T) {
	oauth := newNotionOAuth(NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"})
	url, err := oauth.buildAuthorizeURL("client-1", "http://localhost/oauth/notion/callback", "state-1", "verifier-1")
	if err != nil {
		t.Fatalf("buildAuthorizeURL() error = %v", err)
	}
	if !strings.Contains(url, "client_id=client-1") || !strings.Contains(url, "code_challenge=") {
		t.Fatalf("buildAuthorizeURL() = %q", url)
	}
	if !strings.Contains(url, "resource=https%3A%2F%2Fmcp.notion.com") {
		t.Fatalf("expected resource parameter, got %q", url)
	}
}

func TestNotionOAuthRegisterAndExchange(t *testing.T) {
	var registerBody map[string]any
	var tokenValues url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			_ = json.NewDecoder(r.Body).Decode(&registerBody)
			_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "dyn-client"})
		case "/token":
			_ = r.ParseForm()
			tokenValues = r.PostForm
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token":  "access-1",
				"refresh_token": "refresh-1",
				"scope":         "default",
				"workspace_id":  "ws-1",
				"email_domain":  "example.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oauth := newNotionOAuth(NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"})
	oauth.httpClient = server.Client()
	oauth.registerURL = server.URL + "/register"
	oauth.tokenURL = server.URL + "/token"

	clientID, err := oauth.ensureClient(context.Background(), "http://localhost/callback")
	if err != nil || clientID != "dyn-client" {
		t.Fatalf("ensureClient() = (%q, %v)", clientID, err)
	}
	if registerBody["redirect_uris"] == nil {
		t.Fatalf("register body = %#v", registerBody)
	}
	response, err := oauth.exchange(context.Background(), "code-1", "verifier-1", "http://localhost/callback", clientID)
	if err != nil {
		t.Fatalf("exchange() error = %v", err)
	}
	if response.AccessToken != "access-1" || response.RefreshToken != "refresh-1" || len(response.Scopes) != 1 {
		t.Fatalf("exchange() = %+v", response)
	}
	if got := oauth.accountLabel(response); got != "example.com" {
		t.Fatalf("accountLabel() = %q", got)
	}
	if tokenValues.Get("code_verifier") != "verifier-1" {
		t.Fatalf("token form = %#v", tokenValues)
	}
}

func TestNotionOAuthRefresh(t *testing.T) {
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
				"scope":         "default",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oauth := newNotionOAuth(NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"})
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

func TestConfigNotionEnabled(t *testing.T) {
	cfg := Config{
		PublicBaseURL: "https://example.com",
		Notion:        NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"},
	}
	if !cfg.NotionEnabled() {
		t.Fatal("expected notion enabled")
	}
	if !cfg.OAuthEnabled() {
		t.Fatal("expected OAuthEnabled true")
	}
}

func TestNotionAccessTokenRefreshesBeforeExpiry(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	service := &Service{
		Store: store,
		Config: Config{
			PublicBaseURL: "http://127.0.0.1:8765",
			SecretKey:     "test-secret",
			Notion:        NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"},
		},
	}
	ctx := context.Background()
	expired := notionTokenBundle{
		Access:      "stale-access",
		Refresh:     "refresh-1",
		ClientID:    "client-1",
		RedirectURI: "http://127.0.0.1:8765/oauth/notion/callback",
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
	}
	raw, err := encodeNotionTokenBundle(expired)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertToken(ctx, LocalUserID, ProviderNotion, raw, []string{"default"}, "example.com"); err != nil {
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
	service.mutableState().notionOAuth = newNotionOAuth(NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"})
	service.mutableState().notionOAuth.httpClient = server.Client()
	service.mutableState().notionOAuth.tokenURL = server.URL + "/token"

	token, err := service.NotionAccessToken(ctx, LocalUserID)
	if err != nil {
		t.Fatalf("NotionAccessToken() error = %v", err)
	}
	if token != "fresh-access" {
		t.Fatalf("token = %q", token)
	}
	stored, err := store.RawToken(ctx, LocalUserID, ProviderNotion)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := parseNotionTokenBundle(stored)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Access != "fresh-access" || bundle.Refresh != "refresh-2" {
		t.Fatalf("stored bundle = %+v", bundle)
	}
}
