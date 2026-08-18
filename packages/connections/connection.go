// Package connections manages per-user integration credentials and OAuth flows.
package connections

import (
	"errors"
	"fmt"
	"time"
)

const ProviderSlack = "slack"
const ProviderGitHub = "github"
const ProviderClickStack = "clickstack"

const LocalUserID = "local"

// Status describes a stored user connection.
type Status string

const (
	StatusConnected Status = "connected"
	StatusExpired   Status = "expired"
	StatusRevoked   Status = "revoked"
)

// Connection is durable user-owned credentials for one provider.
type Connection struct {
	UserID    string    `json:"user_id"`
	Provider  string    `json:"provider"`
	Status    Status    `json:"status"`
	Scopes    []string  `json:"scopes,omitempty"`
	Account   string    `json:"account,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Plugin describes an OAuth-connectable integration.
type Plugin struct {
	ID          string
	Title       string
	Description string
	Scopes      []string
}

// RequiredError is returned when a tool needs a user connection.
type RequiredError struct {
	Provider string
	Title    string
	AuthURL  string
}

func (e *RequiredError) Error() string {
	if e.Title == "" {
		return fmt.Sprintf("%s is not connected", e.Provider)
	}
	return fmt.Sprintf("%s is not connected", e.Title)
}

var ErrNotConnected = errors.New("connection not found")

// Plugins lists connectable integrations for the current deployment.
func Plugins() []Plugin {
	return []Plugin{
		{
			ID:          ProviderSlack,
			Title:       "Slack",
			Description: "Connect your Slack identity to search files and analyze shared content with your own permissions.",
			Scopes: []string{
				"channels:history", "channels:read",
				"groups:history", "groups:read",
				"im:history", "im:read",
				"mpim:history", "mpim:read",
				"files:read", "search:read", "users:read",
				"chat:write", "im:write",
			},
		},
		{
			ID:          ProviderGitHub,
			Title:       "GitHub",
			Description: "Search PRs, workflow runs, and repository metadata with your own GitHub account.",
			Scopes:      []string{"repo", "read:org", "workflow"},
		},
		{
			ID:          ProviderClickStack,
			Title:       "ClickStack",
			Description: "Query logs, traces, dashboards, and alerts in your team's ClickStack workspace with your own ClickHouse Cloud account.",
			Scopes:      []string{"clickstack:access", "openid", "profile", "email"},
		},
	}
}

func pluginTitle(provider string) string {
	for _, item := range Plugins() {
		if item.ID == provider {
			return item.Title
		}
	}
	return provider
}
