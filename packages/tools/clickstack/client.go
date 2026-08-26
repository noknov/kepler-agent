// Package clickstack wires ClickStack MCP into the agent tool catalog.
package clickstack

import (
	"strings"

	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/mcp"
)

const defaultMCPURL = "https://mcp.clickhouse.cloud/clickstack"

// NewMCPClient builds an MCP client from deployment config and an access token.
func NewMCPClient(cfg config.ClickStackConfig, token string) *mcp.Client {
	url := strings.TrimSpace(cfg.MCPURL)
	if url == "" {
		url = defaultMCPURL
	}
	headers := map[string]string{}
	if id := strings.TrimSpace(cfg.ServiceID); id != "" {
		headers["x-service-id"] = id
	}
	if team := strings.TrimSpace(cfg.TeamID); team != "" {
		headers["x-hdx-team"] = team
	}
	return &mcp.Client{
		ServiceName: "clickstack",
		URL:         url,
		Token:       strings.TrimSpace(token),
		Headers:     headers,
	}
}
