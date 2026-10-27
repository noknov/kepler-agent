package hostedtools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestPolicyForSurfaceEnablesSlackConnectionDeps(t *testing.T) {
	conn := &connections.Service{
		Config: connections.Config{
			Slack: connections.SlackOAuthConfig{
				ClientID:     "id",
				ClientSecret: "secret",
			},
		},
	}
	policy := PolicyForSurface(config.Config{}, SurfaceOptions{
		Name:          "slack",
		AvailableDeps: map[string]bool{"slack": true},
		Connections:   conn,
	})
	if !policy.AvailableDeps["slack-connection"] {
		t.Fatal("expected slack-connection dependency to be available when Slack OAuth is configured")
	}
}

func TestPolicyForSurfaceEnablesClickStackDeps(t *testing.T) {
	policy := PolicyForSurface(config.Config{
		Integrations: config.IntegrationConfig{
			ClickStack: config.ClickStackConfig{ServiceID: "svc-1"},
		},
	}, SurfaceOptions{Name: "slack"})
	if !policy.AvailableDeps["clickstack"] {
		t.Fatal("expected clickstack dependency when CLICKSTACK_SERVICE_ID is configured")
	}
}

func TestPolicyForSurfaceEnablesNotionDeps(t *testing.T) {
	conn := &connections.Service{
		Config: connections.Config{
			PublicBaseURL: "https://example.com",
			Notion:        connections.NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"},
		},
	}
	policy := PolicyForSurface(config.Config{
		Integrations: config.IntegrationConfig{
			Notion: config.NotionConfig{MCPURL: "https://mcp.notion.com/mcp"},
		},
	}, SurfaceOptions{Name: "slack", Connections: conn})
	if !policy.AvailableDeps["notion"] {
		t.Fatal("expected notion dependency when Notion MCP is configured")
	}
	if !policy.AvailableDeps["notion-connection"] {
		t.Fatal("expected notion-connection when Notion OAuth is configured")
	}
}

func TestGitHubOAuthCatalogRequiresTheCallingUsersConnection(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertToken(t.Context(), "other-user", connections.ProviderGitHub, "other-token", nil, "other"); err != nil {
		t.Fatal(err)
	}
	conn := &connections.Service{Store: store, Config: connections.Config{PublicBaseURL: "https://kepler.example", SecretKey: strings.Repeat("k", 32), GitHub: connections.GitHubOAuthConfig{ClientID: "id", ClientSecret: "secret"}}}
	cfg := config.Config{Integrations: config.IntegrationConfig{GitHub: config.GitHubConfig{Token: "operator-token"}}}
	bundle, err := NewCatalog(cfg, safety.WorkspacePolicy{}, safety.CommandPolicy{}, nil, SurfaceOptions{Name: "slack", Connections: conn})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"github-pr_diff", "github-pr_file_diff", "github-workflow_runs", "github-job_logs", "github-dispatch_workflow"} {
		item, ok := bundle.Catalog.Get(name)
		if !ok {
			t.Fatalf("OAuth capability missing: %s", name)
		}
		result, err := item.Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{}`), Scope: tool.Scope{UserID: "unconnected-actor"}})
		if err != nil || result.ErrorCode != "connection_required" {
			t.Fatalf("%s used another identity or operator fallback: %+v %v", name, result, err)
		}
	}
	policy := PolicyForSurface(config.Config{}, SurfaceOptions{Name: "slack", Connections: conn})
	if !policy.AvailableDeps["github"] || !policy.AvailableDeps["github-connection"] {
		t.Fatal("OAuth capability filtered out without static operator token")
	}
}

func TestGitHubOAuthCatalogUsesConnectionAPIOrigin(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer actor-token" || r.URL.Path != "/repos/owner/repo/actions/workflows/ci.yml/runs" {
			t.Errorf("unexpected authorized request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer api.Close()
	operator := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("user credential reached operator API origin") }))
	defer operator.Close()
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertToken(t.Context(), "actor", connections.ProviderGitHub, "actor-token", nil, "actor"); err != nil {
		t.Fatal(err)
	}
	conn := &connections.Service{Store: store, Config: connections.Config{GitHub: connections.GitHubOAuthConfig{ClientID: "id", ClientSecret: "secret", APIBaseURL: api.URL}}}
	cfg := config.Config{Integrations: config.IntegrationConfig{GitHub: config.GitHubConfig{Token: "operator-token", APIBaseURL: operator.URL}}}
	bundle, err := NewCatalog(cfg, safety.WorkspacePolicy{}, safety.CommandPolicy{}, nil, SurfaceOptions{Name: "slack", Connections: conn})
	if err != nil {
		t.Fatal(err)
	}
	item, ok := bundle.Catalog.Get("github-workflow_runs")
	if !ok {
		t.Fatal("missing tool")
	}
	result, err := item.Execute(t.Context(), tool.Call{Arguments: json.RawMessage(`{"repository":"owner/repo","workflow":"ci.yml"}`), Scope: tool.Scope{UserID: "actor"}})
	if err != nil || result.IsError || calls != 1 {
		t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
	}
}
