// Package connections manages per-user integration credentials and OAuth flows.
package connections

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const ProviderSlack = "slack"
const ProviderGitHub = "github"
const ProviderClickStack = "clickstack"
const ProviderGCP = "gcp"
const ProviderNotion = "notion"

const LocalUserID = "local"

// DefaultInstanceID is used by the legacy provider-only APIs. New callers
// should retain an instance id for every user connection, even when a
// provider only has one connection today.
const DefaultInstanceID = "default"

// Status describes a stored user connection.
type Status string

const (
	StatusConnected Status = "connected"
	StatusExpired   Status = "expired"
	StatusRevoked   Status = "revoked"
)

// Connection is durable user-owned credentials for one provider.
type Connection struct {
	UserID     string            `json:"user_id"`
	Provider   string            `json:"provider"`
	InstanceID string            `json:"instance_id,omitempty"`
	Label      string            `json:"label,omitempty"`
	Status     Status            `json:"status"`
	Scopes     []string          `json:"scopes,omitempty"`
	Account    string            `json:"account,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// Plugin describes an OAuth-connectable integration.
type Plugin struct {
	ID               string
	Title            string
	Description      string
	Scopes           []string
	SupportsMultiple bool
	Fields           []PluginField
}

// PluginField is a declaration of user-provided connection configuration.
// Surfaces can render these fields without adding provider-specific branches.
type PluginField struct {
	ID          string
	Label       string
	Description string
	Placeholder string
	Secret      bool
	Required    bool
}

// ConnectionCard is the surface-neutral representation of one connection
// and its available action. Slack, web, and future surfaces can render the
// same state without treating a provider as a singleton.
type ConnectionCard struct {
	Provider   string
	InstanceID string
	Title      string
	Label      string
	Status     Status
	Account    string
	ConnectURL string
}

// ConnectionAction is a declarative chat/UI action. The action is safe to
// render as a button or link and carries no provider-specific presentation
// assumptions.
type ConnectionAction struct {
	Type       string            `json:"type"`
	Provider   string            `json:"provider"`
	InstanceID string            `json:"instance_id,omitempty"`
	Label      string            `json:"label,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	URL        string            `json:"url,omitempty"`
	State      string            `json:"state"`
}

func ConnectionActionFor(required *RequiredError) ConnectionAction {
	if required == nil {
		return ConnectionAction{}
	}
	state := "connect"
	if required.Reauthorize {
		state = "reauthorize"
	} else if strings.TrimSpace(required.AuthURL) == "" {
		// The deployment can expose a provider without enough information to
		// build an OAuth URL yet (for example, a user-declared MCP endpoint).
		// Surfaces should render an add/configure action instead of a dead link.
		state = "configure"
	}
	return ConnectionAction{
		Type:       "connection",
		Provider:   required.Provider,
		InstanceID: connectionInstanceID(required.InstanceID),
		Label:      required.Title,
		Metadata:   cloneMetadata(required.Metadata),
		URL:        required.AuthURL,
		State:      state,
	}
}

// RequiredError is returned when a tool needs a user connection.
type RequiredError struct {
	Provider    string
	InstanceID  string
	Title       string
	AuthURL     string
	Reauthorize bool
	Metadata    map[string]string
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
			SupportsMultiple: true,
		},
		{
			ID:               ProviderGitHub,
			Title:            "GitHub",
			Description:      "Search PRs, workflow runs, and repository metadata with your own GitHub account.",
			Scopes:           []string{"repo", "read:org", "workflow"},
			SupportsMultiple: true,
		},
		{
			ID:               ProviderClickStack,
			Title:            "ClickStack",
			Description:      "Query logs, traces, dashboards, and alerts in your team's ClickStack workspace with your own ClickHouse Cloud account.",
			Scopes:           []string{"clickstack:access", "openid", "profile", "email"},
			SupportsMultiple: true,
			Fields:           []PluginField{{ID: "mcp_url", Label: "Server URL", Description: "The ClickStack MCP server to connect.", Placeholder: "https://mcp.clickhouse.cloud/clickstack", Required: false}},
		},
		{
			ID:               ProviderGCP,
			Title:            "Google Cloud",
			Description:      "View Cloud Logging, Cloud Run services, and GKE clusters with your own Google account (read-only).",
			Scopes:           []string{"logging.read", "cloud-platform.read-only"},
			SupportsMultiple: true,
		},
		{
			ID:               ProviderNotion,
			Title:            "Notion",
			Description:      "Search, read, and update Notion pages and databases with your own account via Notion MCP.",
			Scopes:           []string{"default"},
			SupportsMultiple: true,
		},
	}
}

var instanceIDPattern = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizeInstanceID turns a user-facing label into a stable, URL-safe
// identifier. Empty labels intentionally map to the legacy default instance.
func NormalizeInstanceID(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	label = instanceIDPattern.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-_")
	if label == "" {
		return DefaultInstanceID
	}
	return label
}

// NewInstanceID returns an opaque id for a newly-added connection. Display
// labels are intentionally not identity: two connections may share a label or
// omit one without overwriting each other.
func NewInstanceID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate connection instance id: %w", err)
	}
	return "i-" + hex.EncodeToString(raw[:]), nil
}

// InstanceDisplayName is presentation-only. It never participates in
// identity or routing, but gives model and human-facing tool names a useful
// hint when a connection has no explicit label.
func InstanceDisplayName(connection Connection) string {
	if label := strings.TrimSpace(connection.Label); label != "" {
		return label
	}
	if raw := strings.TrimSpace(connection.Metadata["mcp_url"]); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
			return parsed.Hostname()
		}
	}
	if instanceID := strings.TrimSpace(connection.InstanceID); instanceID != "" && instanceID != DefaultInstanceID {
		return instanceID
	}
	return "default"
}

func pluginByID(provider string) (Plugin, bool) {
	for _, item := range Plugins() {
		if item.ID == provider {
			return item, true
		}
	}
	return Plugin{}, false
}

// FindPlugin returns the declarative definition for a provider.
func FindPlugin(provider string) (Plugin, bool) { return pluginByID(provider) }

func pluginTitle(provider string) string {
	if item, ok := pluginByID(provider); ok {
		return item.Title
	}
	return provider
}

// StatusMapByInstance preserves every connection instead of collapsing a
// provider to one map entry. The old StatusMap remains for legacy callers.
func StatusMapByInstance(items []Connection) map[string]Connection {
	out := make(map[string]Connection, len(items))
	for _, item := range items {
		out[item.Provider+"\x00"+connectionInstanceID(item.InstanceID)] = item
	}
	return out
}
