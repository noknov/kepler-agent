// Package playwright provides browser automation tools backed by a remote Playwright MCP server.
package playwright

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	// DefaultArgs are fixed JSON key/value pairs merged into every call before
	// forwarding to the remote tool. Default values take precedence over
	// user-supplied args, so they act as fixed parameters the LLM cannot override.
	DefaultArgs json.RawMessage
}

func (t MCPTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(t.LocalName, t.Description, t.Parameters)
}

func (t MCPTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Playwright MCP is not configured: PLAYWRIGHT_MCP_URL is required")
	}
	args := mergeDefaultArgs(t.DefaultArgs, raw)
	session, err := getOrCreateSession(ctx, t.Client.MCP, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.Client.MCP.CallTool(ctx, session, t.RemoteName, args)
	if err != nil && isSessionExpired(err) {
		session, err = createSession(ctx, t.Client.MCP, rt.Cache)
		if err != nil {
			return registry.Result{}, err
		}
		out, err = t.Client.MCP.CallTool(ctx, session, t.RemoteName, args)
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

// mergeDefaultArgs merges fixed default args into user-supplied args.
// Keys present in defaults always win, acting as fixed parameters the caller cannot override.
// If defaults is nil/empty, the original userArgs is returned unchanged.
func mergeDefaultArgs(defaults, userArgs json.RawMessage) json.RawMessage {
	if len(defaults) == 0 {
		return userArgs
	}
	var d, u map[string]json.RawMessage
	if err := json.Unmarshal(defaults, &d); err != nil {
		return userArgs
	}
	if err := json.Unmarshal(userArgs, &u); err != nil {
		u = map[string]json.RawMessage{}
	}
	merged := make(map[string]json.RawMessage, len(u)+len(d))
	for k, v := range u {
		merged[k] = v
	}
	for k, v := range d { // defaults override user args
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return userArgs
	}
	return b
}

// NavigateTool wraps browser_navigate with automatic about:blank recovery.
//
// When the page load triggers an OIDC signinPopup() (or any other mechanism
// that opens a new browser window), Playwright MCP shifts its "active page"
// focus to the popup, which starts as about:blank. Subsequent snapshot/screenshot
// calls then see the popup rather than the main app.
//
// After each navigation this tool evaluates window.location.href. If the result
// is about:blank it lists all open tabs, locates the first non-blank one, and
// switches focus back to it — returning an inline notice so the LLM knows a
// recovery was performed.
type NavigateTool struct {
	Client *Client
}

func (t NavigateTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("pw-navigate", "", registry.ObjectSchema([]string{"url"}, map[string]any{
		"url": map[string]any{"type": "string", "description": "Absolute URL to navigate to."},
	}))
}

func (t NavigateTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Playwright MCP is not configured: PLAYWRIGHT_MCP_URL is required")
	}
	session, err := getOrCreateSession(ctx, t.Client.MCP, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.Client.MCP.CallTool(ctx, session, "browser_navigate", raw)
	if err != nil && isSessionExpired(err) {
		session, err = createSession(ctx, t.Client.MCP, rt.Cache)
		if err != nil {
			return registry.Result{}, err
		}
		out, err = t.Client.MCP.CallTool(ctx, session, "browser_navigate", raw)
	}
	if err != nil {
		return registry.Result{}, err
	}

	// Check for about:blank redirect caused by OIDC popup or similar JS redirect.
	hrefOut, evalErr := t.Client.MCP.CallTool(ctx, session, "browser_evaluate",
		json.RawMessage(`{"expression":"window.location.href"}`))
	if evalErr == nil && strings.Contains(hrefOut, "about:blank") {
		// List all open tabs to find the main page.
		tabsOut, listErr := t.Client.MCP.CallTool(ctx, session, "browser_tabs",
			json.RawMessage(`{"action":"list"}`))
		if listErr == nil {
			idx := findMainTabIndex(tabsOut)
			if idx >= 0 {
				switchArgs := json.RawMessage(`{"action":"select","index":` + strconv.Itoa(idx) + `}`)
				_, _ = t.Client.MCP.CallTool(ctx, session, "browser_tabs", switchArgs)
				return registry.Result{Content: out + "\n\n[Auto-recovery] Page was redirected to about:blank " +
					"(likely OIDC auth popup). Switched focus back to main tab (index " + strconv.Itoa(idx) + "). " +
					"Call pw-snapshot to see current page state. " +
					"If the page still requires authentication, use pw-get_all_pages to inspect open tabs " +
					"or navigate directly to the login URL."}, nil
			}
		}
		return registry.Result{Content: out + "\n\n[Warning] Page redirected to about:blank. " +
			"This is likely an OIDC authentication popup. " +
			"Use pw-get_all_pages to list all open tabs, or navigate directly to the login URL."}, nil
	}

	return registry.Result{Content: out}, nil
}

// findMainTabIndex parses browser_tabs list output and returns the index of the
// first tab whose URL is not about:blank. Returns -1 if none found.
//
// Expected line format from Playwright MCP:
//
//	- 0: (current) [Page Title](https://example.com/)
//	- 1: [Other Page](https://other.example.com/)
func findMainTabIndex(tabsText string) int {
	for _, line := range strings.Split(tabsText, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		if strings.Contains(line, "about:blank") {
			continue
		}
		// Extract the numeric index between "- " and the first ":"
		rest := line[2:]
		colonIdx := strings.IndexByte(rest, ':')
		if colonIdx < 0 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(rest[:colonIdx]))
		if err != nil {
			continue
		}
		return idx
	}
	return -1
}

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
	// pw-navigate uses a custom NavigateTool for about:blank auto-recovery.
	reg.Register(NavigateTool{Client: client})
	for _, tool := range tools(client) {
		reg.Register(tool)
	}
}

func tools(client *Client) []MCPTool {
	return []MCPTool{
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
		{
			Client:      client,
			LocalName:   "pw-get_all_pages",
			RemoteName:  "browser_tabs",
			DefaultArgs: json.RawMessage(`{"action":"list"}`),
			Parameters:  registry.ObjectSchema(nil, map[string]any{}),
		},
		{
			Client:     client,
			LocalName:  "pw-switch_page",
			RemoteName: "browser_tabs",
			// "action":"select" is injected by DefaultArgs; the LLM only needs to supply "index".
			DefaultArgs: json.RawMessage(`{"action":"select"}`),
			Parameters: registry.ObjectSchema([]string{"index"}, map[string]any{
				"index": map[string]any{"type": "number", "description": "Tab index to switch to (from pw-get_all_pages output)."},
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
