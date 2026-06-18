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
	if err != nil && isSessionExpired(err) {
		session, err = createSession(ctx, t.Client.MCP, rt.Cache)
		if err != nil {
			return registry.Result{}, err
		}
		out, err = t.Client.MCP.CallTool(ctx, session, t.RemoteName, raw)
	}
	if err != nil {
		return registry.Result{}, err
	}
	// Image data URIs must not pass through LLM context (multi-MB base64 strings cause
	// context overflows and LLM timeouts). The Playwright MCP result is typically a
	// concatenation of a text summary followed by a "data:image/..." URI on its own line.
	// We extract the URI (wherever it appears), cache it, and return only the text part.
	if dataURI, textPart := extractDataURI(out); dataURI != "" {
		if rt.Cache != nil {
			rt.Cache.Set(screenshotCacheKey, dataURI)
		}
		sizeKB := len(dataURI) * 3 / 4 / 1024 // approximate decoded size
		summary := strings.TrimSpace(textPart)
		if summary != "" {
			summary += "\n"
		}
		summary += fmt.Sprintf("Screenshot captured (~%dKB). Call slack-send_screenshot to share it in the Slack thread.", sizeKB)
		return registry.Result{Content: summary}, nil
	}
	return registry.Result{Content: out}, nil
}

// extractDataURI splits s into the first data:image/... URI found and the remaining text.
// Returns ("", s) if no data URI is present.
func extractDataURI(s string) (dataURI, rest string) {
	idx := strings.Index(s, "data:image/")
	if idx < 0 {
		return "", s
	}
	// The data URI runs to end of the line (or end of string).
	end := strings.IndexByte(s[idx:], '\n')
	if end < 0 {
		return s[idx:], strings.TrimSpace(s[:idx])
	}
	return s[idx : idx+end], strings.TrimSpace(s[:idx])
}

// ScreenshotCacheKey is the RuntimeCache key used to pass the latest screenshot
// from pw-screenshot to slack-send_screenshot without putting base64 in LLM context.
const ScreenshotCacheKey = "pw-screenshot-latest"

const screenshotCacheKey = ScreenshotCacheKey

// isSessionExpired reports whether err indicates the MCP session has expired or is unknown.
// Playwright MCP returns HTTP 404 for unknown sessions; some implementations use JSON-RPC -32001.
func isSessionExpired(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "status 404") ||
		strings.Contains(msg, "-32001") ||
		strings.Contains(msg, "Session not found") ||
		strings.Contains(msg, "session not found")
}

// RegisterAll registers all Playwright browser tools into the registry.
// It is a no-op when the client is not configured (PLAYWRIGHT_MCP_URL unset).
func RegisterAll(reg *registry.Registry, client *Client) {
	if client == nil || !client.enabled() {
		return
	}
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
				"url": map[string]any{"type": "string", "description": "Absolute URL to navigate to."},
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
			Parameters: registry.ObjectSchema([]string{"target"}, map[string]any{
				"target":  map[string]any{"type": "string", "description": "Element ref from pw-snapshot (e.g. s1e4)."},
				"element": map[string]any{"type": "string", "description": "Human-readable description of the element, for logging."},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-type",
			RemoteName: "browser_type",
			Parameters: registry.ObjectSchema([]string{"target", "text"}, map[string]any{
				"target":  map[string]any{"type": "string", "description": "Element ref from pw-snapshot (e.g. s1e4)."},
				"element": map[string]any{"type": "string", "description": "Human-readable description of the element, for logging."},
				"text":    map[string]any{"type": "string", "description": "Text to type into the element."},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-fill_form",
			RemoteName: "browser_fill_form",
			Parameters: registry.ObjectSchema([]string{"fields"}, map[string]any{
				"fields": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							// "ref" is the field name used by Playwright MCP for per-field element refs.
							"ref":     map[string]any{"type": "string", "description": "Element ref from pw-snapshot."},
							"element": map[string]any{"type": "string", "description": "Human-readable element description."},
							"name":    map[string]any{"type": "string", "description": "Field label or name."},
							"type":    map[string]any{"type": "string", "enum": []string{"textbox", "checkbox", "radio", "combobox", "slider"}},
							"value":   map[string]any{"type": "string", "description": "Value to set."},
						},
						"required": []string{"ref", "name", "type", "value"},
					},
				},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-screenshot",
			RemoteName: "browser_take_screenshot",
			// "type" is optional; omitting it defaults to PNG on the server side.
			Parameters: registry.ObjectSchema(nil, map[string]any{
				"type":     map[string]any{"type": "string", "enum": []string{"png", "jpeg"}, "description": "Image format. Defaults to png."},
				"fullPage": map[string]any{"type": "boolean", "description": "Capture the full scrollable page instead of just the viewport."},
				"filename": map[string]any{"type": "string", "description": "Optional filename hint for the screenshot."},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-press_key",
			RemoteName: "browser_press_key",
			Parameters: registry.ObjectSchema([]string{"key"}, map[string]any{
				"key": map[string]any{"type": "string", "description": "Key name (e.g. Enter, Tab, Escape, ArrowDown)."},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-wait",
			RemoteName: "browser_wait_for",
			Parameters: registry.ObjectSchema(nil, map[string]any{
				"text":     map[string]any{"type": "string", "description": "Wait until this text appears on the page."},
				"textGone": map[string]any{"type": "string", "description": "Wait until this text disappears from the page."},
				"time":     map[string]any{"type": "number", "description": "Time to wait in seconds."},
			}),
		},
		{
			Client:     client,
			LocalName:  "pw-evaluate",
			RemoteName: "browser_evaluate",
			Parameters: registry.ObjectSchema([]string{"expression"}, map[string]any{
				// Playwright MCP evaluates a JavaScript expression string in the browser context.
				// Use standard browser globals (document, window, localStorage) — not Playwright's page object.
				"expression": map[string]any{"type": "string", "description": "JavaScript expression to evaluate in the browser context, e.g. \"document.title\" or \"localStorage.getItem('token')\"."},
			}),
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
