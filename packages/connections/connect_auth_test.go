package connections

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConnectURLSigned(t *testing.T) {
	service := Service{
		Config: Config{
			PublicBaseURL: "https://example.com",
			SecretKey:     "test-secret",
		},
	}
	connectURL, err := service.ConnectURL("U123", ProviderNotion)
	if err != nil {
		t.Fatalf("ConnectURL() error = %v", err)
	}
	if !strings.Contains(connectURL, "/oauth/notion/connect?") || !strings.Contains(connectURL, "user_id=U123") || !strings.Contains(connectURL, "sig=") {
		t.Fatalf("ConnectURL() = %q", connectURL)
	}
}

func TestHandleConnectCreatesOAuthState(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	service := Service{
		Store: store,
		Config: Config{
			PublicBaseURL: "http://127.0.0.1:8765",
			SecretKey:     "test-secret",
			Notion:        NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"},
		},
	}
	connectURL, err := service.ConnectURL(LocalUserID, ProviderNotion)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(connectURL)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil)
	rec := httptest.NewRecorder()

	oauth := newNotionOAuth(NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/register" {
			_, _ = w.Write([]byte(`{"client_id":"dyn-client"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	oauth.httpClient = server.Client()
	oauth.registerURL = server.URL + "/register"
	service.state = &serviceState{notionOAuth: oauth}

	service.HandleConnect(rec, req, ProviderNotion)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "mcp.notion.com/authorize") {
		t.Fatalf("location = %q", location)
	}
	authorizeURL, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeURL.Query().Get("state")
	if state == "" {
		t.Fatal("expected oauth state in authorize redirect")
	}
	_, provider, meta, err := store.PeekOAuthState(context.Background(), state)
	if err != nil {
		t.Fatalf("PeekOAuthState() error = %v", err)
	}
	if provider != ProviderNotion || meta.CodeVerifier == "" {
		t.Fatalf("stored oauth state = (%q, %+v)", provider, meta)
	}
}

func TestVerifyConnectURLRejectsTampering(t *testing.T) {
	service := Service{Config: Config{SecretKey: "test-secret"}}
	exp := time.Now().UTC().Add(time.Minute).Unix()
	sig, err := service.signConnectURL("U123", ProviderNotion, exp)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.verifyConnectURL("U999", ProviderNotion, strconv.FormatInt(exp, 10), sig); err == nil {
		t.Fatal("expected tampered user id to be rejected")
	}
}
