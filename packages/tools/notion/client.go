// Package notion wires Notion MCP into the agent tool catalog.
package notion

import (
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/mcp"
	"github.com/noknov/kepler-agent/packages/safety"
)

const defaultMCPURL = "https://mcp.notion.com/mcp"

// NewMCPClient builds an MCP client from deployment config and an access token.
func NewMCPClient(cfg config.NotionConfig, token string) *mcp.Client {
	url := strings.TrimSpace(cfg.MCPURL)
	if url == "" {
		url = defaultMCPURL
	}
	return &mcp.Client{
		ServiceName: "notion",
		URL:         url,
		Token:       strings.TrimSpace(token),
		HTTP:        safety.SafeHTTPClient(60 * time.Second),
	}
}
