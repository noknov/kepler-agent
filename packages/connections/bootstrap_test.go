package connections

import (
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/config"
)

func TestGitHubOAuthDisabledWhenServerTokenConfigured(t *testing.T) {
	service := NewServiceFromConfig(nil, config.Config{
		Connections: config.ConnectionsConfig{
			GitHubClientID:     "oauth-id",
			GitHubClientSecret: "oauth-secret",
		},
		Integrations: config.IntegrationConfig{
			GitHub: config.GitHubConfig{Token: "ghp-test"},
		},
	})
	if service.Config.GitHubEnabled() {
		t.Fatal("expected GitHub OAuth to be disabled when GITHUB_TOKEN is configured")
	}
}
