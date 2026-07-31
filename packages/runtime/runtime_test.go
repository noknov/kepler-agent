package runtime

import (
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/safety"
)

func TestNewAgentRuntimeCarriesMaxOutputTokens(t *testing.T) {
	cfg := LocalCLIConfig()
	cfg.LLM.MaxOutputTokens = 8192
	rt := NewAgentRuntime(cfg, nil, nil, nil, nil)
	if rt.Runner.MaxTokens != 8192 {
		t.Fatalf("Runner.MaxTokens = %d, want 8192", rt.Runner.MaxTokens)
	}
}

func TestToolRegistryUsesIntegrationConfigForManualConfig(t *testing.T) {
	cfg := config.Config{
		Tools: config.ToolConfig{
			CommandTimeout: 30 * time.Second,
		},
		Integrations: config.IntegrationConfig{
			GitHub: config.GitHubConfig{
				Token:      "github-token",
				APIBaseURL: "https://api.github.com",
			},
			WebSearch: config.WebSearchConfig{
				Provider: "duckduckgo",
			},
		},
	}
	reg := NewToolRegistry(cfg, nil, nil, nil, nil, "", safety.WorkspacePolicy{}, safety.CommandPolicy{}, nil)
	if !reg.ActivateTool("github-pr_diff") {
		t.Fatal("expected github-pr_diff to activate when only legacy ToolConfig GitHub token is set")
	}
}
