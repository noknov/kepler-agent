package connections

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClickStackOAuthAuthorizeURL(t *testing.T) {
	oauth := newClickStackOAuth(ClickStackOAuthConfig{MCPURL: "https://mcp.clickhouse.cloud/clickstack"})
	url, err := oauth.buildAuthorizeURL("client-1", "http://localhost/oauth/clickstack/callback", "state-1", "verifier-1")
	if err != nil {
		t.Fatalf("authorizeURL() error = %v", err)
	}
	if !strings.Contains(url, "client_id=client-1") || !strings.Contains(url, "code_challenge=") {
		t.Fatalf("authorizeURL() = %q", url)
	}
	if !strings.Contains(url, "resource=https%3A%2F%2Fmcp.clickhouse.cloud%2Fclickstack") {
		t.Fatalf("expected resource parameter, got %q", url)
	}
}

func TestClickStackOAuthRegisterAndExchange(t *testing.T) {
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
				"id_token":      testIDToken(t, map[string]string{"email": "user@example.com"}),
				"scope":         "clickstack:access openid",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oauth := newClickStackOAuth(ClickStackOAuthConfig{MCPURL: "https://example.test/clickstack"})
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
	access, refresh, idToken, scopes, err := oauth.exchange(context.Background(), "code-1", "verifier-1", "http://localhost/callback", clientID)
	if err != nil {
		t.Fatalf("exchange() error = %v", err)
	}
	if access != "access-1" || refresh != "refresh-1" || idToken == "" || len(scopes) != 2 {
		t.Fatalf("exchange() = (%q, %q, %q, %#v)", access, refresh, idToken, scopes)
	}
	if got := oauth.accountLabel(idToken); got != "user@example.com" {
		t.Fatalf("accountLabel() = %q", got)
	}
	if tokenValues.Get("code_verifier") != "verifier-1" {
		t.Fatalf("token form = %#v", tokenValues)
	}
}

func testIDToken(t *testing.T, claims map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}
