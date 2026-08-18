package connections

import (
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
			GitHub: GitHubOAuthConfig{
				ClientID:     cfg.Connections.GitHubClientID,
				ClientSecret: cfg.Connections.GitHubClientSecret,
				APIBaseURL:   cfg.Integrations.GitHub.APIBaseURL,
			},
		},
	}
}
