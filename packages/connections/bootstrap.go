package connections

import (
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/config"
)

func NewServiceFromConfig(store Store, cfg config.Config) Service {
	return Service{
		Store: store,
		Config: Config{
			PublicBaseURL: cfg.Connections.PublicBaseURL,
			SecretKey:     cfg.Connections.EncryptionKey,
			Slack: SlackOAuthConfig{
				ClientID:     cfg.Connections.SlackClientID,
				ClientSecret: cfg.Connections.SlackClientSecret,
			},
			GitHub:       githubOAuthConfig(cfg),
			ClickStack: ClickStackOAuthConfig{
				MCPURL:    cfg.Integrations.ClickStack.MCPURL,
				ServiceID: cfg.Integrations.ClickStack.ServiceID,
			},
			GCP: GCPOAuthConfig{
				ClientID:     cfg.Connections.GCPOAuthClientID,
				ClientSecret: cfg.Connections.GCPOAuthClientSecret,
			},
			Notion: NotionOAuthConfig{
				ClientID:     cfg.Connections.NotionOAuthClientID,
				ClientSecret: cfg.Connections.NotionOAuthClientSecret,
			},
		},
	}
}

func githubOAuthConfig(cfg config.Config) GitHubOAuthConfig {
	oauth := GitHubOAuthConfig{
		ClientID:     cfg.Connections.GitHubClientID,
		ClientSecret: cfg.Connections.GitHubClientSecret,
		APIBaseURL:   cfg.Integrations.GitHub.APIBaseURL,
	}
	// Server PAT mode: shared operator token in GITHUB_TOKEN takes precedence over OAuth.
	if strings.TrimSpace(cfg.Integrations.GitHub.Token) != "" {
		oauth.ClientID = ""
		oauth.ClientSecret = ""
	}
	return oauth
}
