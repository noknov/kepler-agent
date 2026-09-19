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

func TestConnectURLContextIsSigned(t *testing.T) {
	service := Service{Config: Config{PublicBaseURL: "https://example.com", SecretKey: "test-secret"}}
	connectURL, err := service.ConnectURLForInstanceWithContext(
		"U123", ProviderClickStack, "i-prod", "Production", map[string]string{"mcp_url": "https://clickstack.example/mcp"},
		ConnectionContext{Origin: "chat", ReturnContext: `{"channel":"C1","thread_ts":"171.1"}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(connectURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("origin") != "chat" || query.Get("return_context") == "" {
		t.Fatalf("connection context query = %v", query)
	}
	metadata := map[string]string{"mcp_url": "https://clickstack.example/mcp"}
	if err := service.verifyConnectURLForInstanceWithContext("U123", ProviderClickStack, "i-prod", "Production", metadata, ConnectionContext{Origin: "chat", ReturnContext: query.Get("return_context")}, query.Get("exp"), query.Get("sig")); err != nil {
		t.Fatalf("verify context URL: %v", err)
	}
	if err := service.verifyConnectURLForInstanceWithContext("U123", ProviderClickStack, "i-prod", "Production", metadata, ConnectionContext{Origin: "app_home", ReturnContext: query.Get("return_context")}, query.Get("exp"), query.Get("sig")); err == nil {
		t.Fatal("expected origin tampering to be rejected")
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
	connectURL, err := service.ConnectURLForInstanceWithContext(LocalUserID, ProviderNotion, "i-chat", "Work", nil, ConnectionContext{Origin: "chat", ReturnContext: `{"channel":"C1","thread_ts":"171.1"}`})
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
	if provider != ProviderNotion || meta.CodeVerifier == "" || meta.InstanceID != "i-chat" || meta.Label != "Work" || meta.Origin != "chat" || meta.ReturnContext == "" {
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

func TestConnectURLForInstanceSignsAndPreservesMetadata(t *testing.T) {
	service := Service{Config: Config{PublicBaseURL: "https://example.com", SecretKey: "test-secret"}}
	connectURL, err := service.ConnectURLForInstance("U1", ProviderClickStack, "i-prod", "Production", map[string]string{"mcp_url": "https://prod.example/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(connectURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("instance_id") != "i-prod" || query.Get("label") != "Production" || !strings.Contains(query.Get("metadata"), "mcp_url") {
		t.Fatalf("connection action query = %v", query)
	}
	if err := service.verifyConnectURLForInstance("U1", ProviderClickStack, query.Get("instance_id"), query.Get("label"), map[string]string{"mcp_url": "https://prod.example/mcp"}, query.Get("exp"), query.Get("sig")); err != nil {
		t.Fatalf("verifyConnectURLForInstance() error = %v", err)
	}
	if err := service.verifyConnectURLForInstance("U1", ProviderClickStack, query.Get("instance_id"), query.Get("label"), map[string]string{"mcp_url": "https://other.example/mcp"}, query.Get("exp"), query.Get("sig")); err == nil {
		t.Fatal("expected metadata tampering to be rejected")
	}
}

func TestConnectURLRejectsPrivateCustomClickStackEndpoint(t *testing.T) {
	service := Service{Config: Config{PublicBaseURL: "https://example.com", SecretKey: "test-secret"}}
	for _, raw := range []string{
		"http://localhost:4318/mcp",
		"http://127.0.0.1:4318/mcp",
		"http://169.254.169.254/latest/meta-data",
	} {
		if _, err := service.ConnectURLForInstance("U1", ProviderClickStack, "i-test", "test", map[string]string{"mcp_url": raw}); err == nil {
			t.Fatalf("ConnectURLForInstance(%q) succeeded for private endpoint", raw)
		}
	}
}
