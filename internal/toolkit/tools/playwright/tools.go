// Package playwright provides browser automation tools backed by a remote Playwright MCP server.
package playwright

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/mcp"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

// Client wraps a shared MCP client with Playwright-specific behavior.
type Client struct {
	MCP *mcp.Client
}

func (c *Client) enabled() bool {
	return c.MCP != nil && strings.TrimSpace(c.MCP.URL) != ""
}

// MCPTool maps a local tool name to a remote Playwright MCP tool.
type MCPTool struct {
	Client      *Client
	LocalName   string
	RemoteName  string
	Description string
	Parameters  map[string]any
}

func (t MCPTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(t.LocalName, t.Description, t.Parameters)
}

func (t MCPTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Playwright MCP is not configured: PLAYWRIGHT_MCP_URL is required")
	}
	session, err := getOrCreateSession(ctx, t.Client.MCP, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.Client.MCP.CallTool(ctx, session, t.RemoteName, raw)
	if err != nil && strings.Contains(err.Error(), "Session not found") {
		session, err = createSession(ctx, t.Client.MCP, rt.Cache)
		if err != nil {
			return registry.Result{}, err
		}
		out, err = t.Client.MCP.CallTool(ctx, session, t.RemoteName, raw)
	}
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}

// RegisterAll registers all Playwright browser tools into the registry.
func RegisterAll(reg *registry.Registry, client *Client) {
	for _, tool := range tools(client) {
		reg.Register(tool)
	}
}

func tools(client *Client) []MCPTool {
	return []MCPTool{
		{
			Client:     client,
			LocalName:  "pw-navigate",
			RemoteName: "browser_navigate",
			Parameters: registry.ObjectSchema([]string{"url"}, map[string]any{
				"url": map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-snapshot",
			RemoteName: "browser_snapshot",
			Parameters: registry.ObjectSchema(nil, map[string]any{}),
		},
		{
			Client:     client,
			LocalName:  "pw-click",
			RemoteName: "browser_click",
			Parameters: registry.ObjectSchema([]string{"element", "ref"}, map[string]any{
				"element": map[string]any{"type": "string", "description": ""},
				"ref":     map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-type",
			RemoteName: "browser_type",
			Parameters: registry.ObjectSchema([]string{"element", "ref", "text"}, map[string]any{
				"element": map[string]any{"type": "string", "description": ""},
				"ref":     map[string]any{"type": "string", "description": ""},
				"text":    map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-fill",
			RemoteName: "browser_fill",
			Parameters: registry.ObjectSchema([]string{"element", "ref", "value"}, map[string]any{
				"element": map[string]any{"type": "string", "description": ""},
				"ref":     map[string]any{"type": "string", "description": ""},
				"value":   map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-screenshot",
			RemoteName: "browser_take_screenshot",
			Parameters: registry.ObjectSchema([]string{"name"}, map[string]any{
				"name": map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-press_key",
			RemoteName: "browser_press_key",
			Parameters: registry.ObjectSchema([]string{"key"}, map[string]any{
				"key": map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-wait",
			RemoteName: "browser_wait_for",
			Parameters: registry.ObjectSchema(nil, map[string]any{
				"text": map[string]any{"type": "string", "description": ""},
				"time": map[string]any{"type": "number", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-evaluate",
			RemoteName: "browser_evaluate",
			Parameters: registry.ObjectSchema([]string{"expression"}, map[string]any{
				"expression": map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-get_url",
			RemoteName: "browser_get_url",
			Parameters: registry.ObjectSchema(nil, map[string]any{}),
		},
	}
}

func getOrCreateSession(ctx context.Context, client *mcp.Client, cache *registry.RuntimeCache) (mcp.Session, error) {
	key := client.SessionKey()
	if cache != nil {
		if cached, ok := cache.Get(key); ok {
			if s, ok := cached.(mcp.Session); ok && s.Initialized {
				return s, nil
			}
		}
	}
	return createSession(ctx, client, cache)
}

func createSession(ctx context.Context, client *mcp.Client, cache *registry.RuntimeCache) (mcp.Session, error) {
	s, err := client.Initialize(ctx)
	if err != nil {
		return mcp.Session{}, err
	}
	if cache != nil {
		cache.Set(client.SessionKey(), s)
	}
	return s, nil
}
