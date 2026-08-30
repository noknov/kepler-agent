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

func TestModelDisplayName(t *testing.T) {
	cases := map[string]string{
		"ox-alpha-free": "Ox Alpha",
		"mimo-v2.5":     "MiMo V2.5",
		"gpt-5.6-luna":  "GPT-5.6 Luna",
		"glm-5.2":       "GLM 5.2",
		"hy3":           "Hy3",
	}
	for model, want := range cases {
		if got := modelDisplayName(model); got != want {
			t.Fatalf("modelDisplayName(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestViewShowsModelDisplayNamesWithoutCodeFormatting(t *testing.T) {
	controller := Controller{
		Cfg: config.Config{
			LLM: config.LLMConfig{
				Model:           "deepseek-v4-flash-vision-exp",
				MultimodalModel: "gpt-5.6-luna",
				SecondaryModel:  "mimo-v2.5",
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
	if !strings.Contains(body, "DeepSeek V4 Flash Vision Exp") {
		t.Fatalf("expected DeepSeek V4 Flash Vision Exp in view, got %s", body)
	}
	if !strings.Contains(body, "MiMo V2.5") {
		t.Fatalf("expected MiMo V2.5 in view, got %s", body)
	}
	if !strings.Contains(body, "Explorer Model") {
		t.Fatalf("expected explorer model label, got %s", body)
	}
	if strings.Contains(body, "Active-turn") || strings.Contains(body, "Image Model") || strings.Contains(body, "toggle_conversation_mode") {
		t.Fatalf("expected no active-turn or image model fields, got %s", body)
	}
	if strings.Contains(body, "deepseek-v4-flash-vision-exp") || strings.Contains(body, "gpt-5.6-luna") || strings.Contains(body, "mimo-v2.5") || strings.Contains(body, "`") {
		t.Fatalf("expected no code model names or backticks, got %s", body)
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
