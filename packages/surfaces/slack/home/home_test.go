package slackhome

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/safety"
)

type stubPublisher struct {
	userID string
	view   map[string]any
	calls  int
}

func (s *stubPublisher) PublishHome(_ context.Context, userID string, view map[string]any) error {
	s.userID = userID
	s.view = view
	s.calls++
	return nil
}

func TestModelDisplayNameShowsRawModelID(t *testing.T) {
	cases := map[string]string{
		"ox-alpha-free":  "ox-alpha-free",
		"gpt-5.6-luna":   "gpt-5.6-luna",
		"deepseek-flash": "deepseek-flash",
		"glm-5.3-flash":  "glm-5.3-flash",
		"vendor/model-x": "vendor/model-x",
		"  glm-5.2  ":    "glm-5.2",
		"":               "Unknown",
	}
	for model, want := range cases {
		if got := modelDisplayName(model); got != want {
			t.Fatalf("modelDisplayName(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestViewShowsRawModelIDAndHidesSecondary(t *testing.T) {
	controller := Controller{
		Cfg: config.Config{
			LLM: config.LLMConfig{
				Model:          "deepseek-flash",
				SecondaryModel: "mimo-v2.5",
			},
		},
		Access: safety.AccessPolicy{},
	}
	view := controller.View("U123")
	blocks, ok := view["blocks"].([]map[string]any)
	if !ok {
		t.Fatal("expected blocks in view")
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "deepseek-flash") {
		t.Fatalf("expected raw primary model ID in view, got %s", body)
	}
	if strings.Contains(body, "mimo-v2.5") || strings.Contains(body, "MiMo V2.5") || strings.Contains(body, "Explorer Model") {
		t.Fatalf("expected secondary model hidden from view, got %s", body)
	}
	if !strings.Contains(body, "Capabilities") || !strings.Contains(body, "Code Review") || !strings.Contains(body, "Review GitHub pull requests with a multi-agent workflow.") {
		t.Fatalf("expected conversational code review capability, got %s", body)
	}
	if strings.Contains(body, "secondary model") || strings.Contains(body, "ordinary questions remain") {
		t.Fatalf("expected concise code review capability, got %s", body)
	}
	webSearchIndex := strings.Index(body, "toggle_web_search")
	rulesIndex := strings.Index(body, "manage_rules")
	skillsIndex := strings.Index(body, "manage_skills")
	capabilitiesIndex := strings.LastIndex(body, "Capabilities")
	if webSearchIndex < 0 || rulesIndex < 0 || skillsIndex < 0 || capabilitiesIndex < 0 || !(webSearchIndex < rulesIndex && rulesIndex < skillsIndex && skillsIndex < capabilitiesIndex) {
		t.Fatalf("expected Web Search before managers and Capabilities last, got %s", body)
	}
	if strings.Contains(body, "Active-turn") || strings.Contains(body, "Image Model") || strings.Contains(body, "toggle_conversation_mode") {
		t.Fatalf("expected no active-turn or image model fields, got %s", body)
	}
	if strings.Contains(body, "`") {
		t.Fatalf("expected no code formatting in view, got %s", body)
	}
}

func TestControllerRequestRefreshFallsBackToPublish(t *testing.T) {
	pub := &stubPublisher{}
	controller := Controller{
		Cfg:   config.Config{},
		Slack: pub,
	}

	if err := controller.RequestRefresh(context.Background(), "U123"); err != nil {
		t.Fatalf("RequestRefresh() error = %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("PublishHome calls = %d, want 1", pub.calls)
	}
	if pub.userID != "U123" {
		t.Fatalf("userID = %q, want U123", pub.userID)
	}
}

func TestConnectionBlocksShowsLocalGCPAsConnectedWithoutButton(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	controller := Controller{
		Cfg: config.Config{
			Integrations: config.IntegrationConfig{
				GCP: config.GCPConfig{DefaultProject: "my-gcp-project"},
			},
		},
		Connections: connections.Service{
			Store: store,
			Config: connections.Config{
				PublicBaseURL: "https://example.com",
			},
		},
	}
	blocks := controller.connectionBlocks("U123")
	if len(blocks) == 0 {
		t.Fatal("expected connection blocks for local GCP credentials")
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "Google Cloud") {
		t.Fatalf("expected Google Cloud in blocks, got %s", body)
	}
	if !strings.Contains(body, "Connected") {
		t.Fatalf("expected Connected status, got %s", body)
	}
	if !strings.Contains(body, "server credentials") {
		t.Fatalf("expected server credentials label, got %s", body)
	}
	if strings.Contains(body, `"text":"Connect"`) || strings.Contains(body, `"text":"Reconnect"`) {
		t.Fatalf("expected no connect button, got %s", body)
	}
}

func TestConnectionBlocksShowsGitHubServerCredentials(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	controller := Controller{
		Cfg: config.Config{
			Integrations: config.IntegrationConfig{
				GitHub: config.GitHubConfig{Token: "ghp-test"},
			},
		},
		Connections: connections.Service{Store: store},
	}
	blocks := controller.connectionBlocks("U123")
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "GitHub") || !strings.Contains(body, "server credentials") {
		t.Fatalf("expected GitHub server credentials block, got %s", body)
	}
}

func TestConnectionBlocksShowsInvalidNotionLegacyToken(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if err := store.UpsertToken(context.Background(), "U123", connections.ProviderNotion, `{"access":"old-token"}`, nil, "old"); err != nil {
		t.Fatal(err)
	}
	controller := Controller{
		Cfg: config.Config{
			Integrations: config.IntegrationConfig{
				Notion: config.NotionConfig{MCPURL: "https://mcp.notion.com/mcp"},
			},
		},
		Connections: connections.Service{
			Store: store,
			Config: connections.Config{
				PublicBaseURL: "https://example.com",
				SecretKey:     "test-secret",
				Notion:        connections.NotionOAuthConfig{MCPURL: "https://mcp.notion.com/mcp"},
			},
		},
	}
	blocks := controller.connectionBlocks("U123")
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "Notion") || !strings.Contains(body, "Invalid") {
		t.Fatalf("expected invalid Notion status, got %s", body)
	}
	if !strings.Contains(body, `"text":"Connect"`) {
		t.Fatalf("expected Connect button, got %s", body)
	}
}

func TestConnectionBlocksShowsInvalidClickStackLegacyOAuthToken(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if err := store.UpsertToken(context.Background(), "U123", connections.ProviderClickStack, `{"access":"old-token"}`, nil, "old"); err != nil {
		t.Fatal(err)
	}
	controller := Controller{
		Cfg: config.Config{
			Integrations: config.IntegrationConfig{
				ClickStack: config.ClickStackConfig{ServiceID: "svc-1"},
			},
		},
		Connections: connections.Service{
			Store: store,
			Config: connections.Config{
				PublicBaseURL: "https://example.com",
				SecretKey:     "test-secret",
				ClickStack:    connections.ClickStackOAuthConfig{ServiceID: "svc-1"},
			},
		},
	}
	blocks := controller.connectionBlocks("U123")
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "ClickStack") || !strings.Contains(body, "Invalid") {
		t.Fatalf("expected invalid ClickStack status, got %s", body)
	}
	if !strings.Contains(body, `"text":"Connect"`) {
		t.Fatalf("expected Connect button, got %s", body)
	}
}

func TestConnectionBlocksShowsYouTrackServerCredentials(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	controller := Controller{
		Cfg: config.Config{
			Integrations: config.IntegrationConfig{
				YouTrack: config.YouTrackConfig{
					URL:   "https://clareai.youtrack.cloud",
					Token: "perm-test",
				},
			},
		},
		Connections: connections.Service{Store: store},
	}
	blocks := controller.connectionBlocks("U123")
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "YouTrack") || !strings.Contains(body, "server credentials") {
		t.Fatalf("expected YouTrack server credentials block, got %s", body)
	}
}

func TestConnectionBlocksAddIntegrationCarriesExplicitOrigin(t *testing.T) {
	legacy := actionButton("legacy_action", "Legacy", "legacy-value", "")
	if got := legacy["value"]; got != "legacy-value" {
		t.Fatalf("non-empty button value = %#v, want legacy-value", got)
	}

	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	controller := Controller{
		Connections: connections.Service{
			Store: store,
			Config: connections.Config{
				PublicBaseURL: "https://example.com",
				SecretKey:     "test-secret",
			},
		},
	}

	blocks := controller.connectionBlocks("U123")
	for _, block := range blocks {
		if block["type"] != "actions" {
			continue
		}
		elements, ok := block["elements"].([]map[string]any)
		if !ok {
			continue
		}
		for _, element := range elements {
			if element["action_id"] != "add_connection" {
				continue
			}
			if got := element["value"]; got != `{"origin":"app_home"}` {
				t.Fatalf("add integration button context value = %#v, want app_home context", got)
			}
			return
		}
	}
	t.Fatal("expected add integration button")
}
