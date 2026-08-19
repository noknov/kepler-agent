package connections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGCPOAuthAuthorizeURL(t *testing.T) {
	oauth := newGCPOAuth(GCPOAuthConfig{ClientID: "client-id", ClientSecret: "secret"})
	url := oauth.buildAuthorizeURL("https://example.test/oauth/gcp/callback", "state-token")
	if !strings.Contains(url, "client_id=client-id") {
		t.Fatalf("authorize url missing client_id: %s", url)
	}
	if !strings.Contains(url, "access_type=offline") {
		t.Fatalf("authorize url missing offline access: %s", url)
	}
	if !strings.Contains(url, "logging.read") || !strings.Contains(url, "cloud-platform.read-only") {
		t.Fatalf("authorize url missing read-only scopes: %s", url)
	}
}

func TestGCPOAuthExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
			"scope":         gcpOAuthScope,
		})
	}))
	defer server.Close()

	oauth := newGCPOAuth(GCPOAuthConfig{ClientID: "client-id", ClientSecret: "secret"})
	oauth.httpClient = server.Client()
	oauth.tokenURL = server.URL + "/token"

	response, err := oauth.exchange(t.Context(), "code-1", "https://example.test/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if response.AccessToken != "access-1" || response.RefreshToken != "refresh-1" {
		t.Fatalf("unexpected token response: %+v", response)
	}
}
