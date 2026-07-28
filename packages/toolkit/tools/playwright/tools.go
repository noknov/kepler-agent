// Package playwright provides browser automation tools backed by a remote Playwright MCP server.
package playwright

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
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
	// StabilizeBefore checks/fixes active page focus before reading page state.
	StabilizeBefore bool
	// StabilizeAfter waits for navigation/display state to settle after an action.
	StabilizeAfter bool
}

func (t MCPTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(t.LocalName, t.Description, t.Parameters)
}

func (t MCPTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Playwright MCP is not configured: PLAYWRIGHT_MCP_URL is required")
	}
	args := mergeDefaultArgs(t.DefaultArgs, normalizeToolArgs(t.LocalName, raw))
	session, err := getOrCreateSession(ctx, t.Client.MCP, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}
	var notices []string
	if t.StabilizeBefore {
		if notice, _ := ensurePageStable(ctx, t.Client.MCP, session, rt.Cache); notice != "" {
			notices = append(notices, notice)
		}
	}
	out, session, err := callToolWithRetry(ctx, t.Client.MCP, rt.Cache, session, t.RemoteName, args)
	if err != nil {
		return registry.Result{}, err
	}
	if t.StabilizeAfter {
		if notice, _ := stabilizeAndCachePage(ctx, t.Client.MCP, session, rt.Cache); notice != "" {
			notices = append(notices, notice)
		}
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
		summary += fmt.Sprintf("Screenshot captured (~%dKB). Call slack-send_screenshot only if the user explicitly asked to see it.", sizeKB)
		summary = appendNotices(summary, notices)
		return registry.Result{Content: summary}, nil
	}
	// Strip internal .playwright-mcp/ file paths from all tool output — not just snapshots.
	// pw-navigate results also contain lines like "[Snapshot](.playwright-mcp/page-xxx.yml)"
	// which cause the LLM to call code-read_file on container-internal paths.
	return registry.Result{Content: appendNotices(sanitizeSnapshotOutput(out), notices)}, nil
}

const snapshotPathNotice = "Note: .playwright-mcp/* paths are internal to the browser container and cannot be read with code-read_file or other workspace file tools. Use element refs from this output (e.g. ref=s1e4) directly with pw-click, pw-type, or pw-fill_form."
const pageStateCacheKeyPrefix = "pw-page-state-"

// lastNavigatedURLCacheKey stores the URL most recently passed to pw-navigate.
// SnapshotTool reads this to perform a cross-domain redirect check that is
// independent of any site-specific URL patterns.
const lastNavigatedURLCacheKey = "pw-last-navigated-url"

type pageState struct {
	Stable bool
	Href   string
}

var (
	rePlaywrightMDLink = regexp.MustCompile(`(?m)\[([^\]]*)\]\(\.playwright-mcp/[^)]+\)`)
	rePlaywrightPath   = regexp.MustCompile(`\.playwright-mcp/[a-zA-Z0-9._-]+`)
)

// sanitizeSnapshotOutput removes internal Playwright MCP file-path references from
// snapshot results so the model does not try to read them via code-read_file.
func sanitizeSnapshotOutput(s string) string {
	if !strings.Contains(s, ".playwright-mcp/") {
		return s
	}
	stripped := false
	out := rePlaywrightMDLink.ReplaceAllStringFunc(s, func(match string) string {
		stripped = true
		sub := rePlaywrightMDLink.FindStringSubmatch(match)
		if len(sub) >= 2 {
			if text := strings.TrimSpace(sub[1]); text != "" {
				return text
			}
		}
		return ""
	})
	out = rePlaywrightPath.ReplaceAllString(out, "")
	out = collapseBlankLines(strings.TrimSpace(out))
	if !stripped {
		return out
	}
	if out != "" {
		return out + "\n\n" + snapshotPathNotice
	}
	return snapshotPathNotice
}

func collapseBlankLines(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		blank := strings.TrimSpace(line) == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, line)
		prevBlank = blank
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
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

func normalizeToolArgs(localName string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return raw
	}
	switch localName {
	case "pw-click", "pw-type":
		if _, hasRef := args["ref"]; !hasRef {
			if target, hasTarget := args["target"]; hasTarget {
				args["ref"] = target
			}
		}
		delete(args, "target")
	}
	b, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return b
}

func mustJSONRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func callToolWithRetry(ctx context.Context, client *mcp.Client, cache *registry.RuntimeCache, session mcp.Session, name string, args json.RawMessage) (string, mcp.Session, error) {
	out, err := client.CallTool(ctx, session, name, args)
	if err != nil && isSessionExpired(err) {
		var createErr error
		session, createErr = createSession(ctx, client, cache)
		if createErr != nil {
			return "", session, createErr
		}
		markPageStateUnknown(cache, client)
		out, err = client.CallTool(ctx, session, name, args)
	}
	return out, session, err
}

func appendNotices(content string, notices []string) string {
	content = strings.TrimSpace(content)
	for _, notice := range notices {
		notice = strings.TrimSpace(notice)
		if notice == "" {
			continue
		}
		if content != "" {
			content += "\n\n"
		}
		content += notice
	}
	return content
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

	var argsMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &argsMap); err != nil {
		argsMap = map[string]json.RawMessage{}
	}
	var requestedURL string
	if urlRaw, ok := argsMap["url"]; ok {
		_ = json.Unmarshal(urlRaw, &requestedURL)
	}
	if err := safety.ValidatePublicHTTPURL(requestedURL); err != nil {
		return registry.Result{}, fmt.Errorf("navigation blocked: %w", err)
	}

	// Cache the requested URL so SnapshotTool can later detect cross-domain
	// redirects without relying on site-specific URL patterns.
	if rt.Cache != nil && requestedURL != "" {
		rt.Cache.Set(lastNavigatedURLCacheKey, requestedURL)
	}

	// Prefer networkidle so Playwright waits at the browser-protocol level
	// until the network goes quiet, catching client-side redirects and SPA
	// auth flows that fire after the initial load event.
	// Pages with continuous background polling (dashboards, SSE streams) will
	// never reach networkidle and the MCP will return a timeout error; in that
	// case we fall back to "load" which is Playwright's default.
	navigateRaw := buildNavigateArgs(argsMap, "networkidle")
	out, session, err := callToolWithRetry(ctx, t.Client.MCP, rt.Cache, session, "browser_navigate", navigateRaw)
	if err != nil && isNavigationTimeout(err) {
		navigateRaw = buildNavigateArgs(argsMap, "load")
		out, session, err = callToolWithRetry(ctx, t.Client.MCP, rt.Cache, session, "browser_navigate", navigateRaw)
	}
	if err != nil {
		return registry.Result{}, err
	}

	out = sanitizeSnapshotOutput(out)
	applyStealthPatch(ctx, t.Client.MCP, session)

	stabilizeNotice, currentHref := stabilizeAndCachePage(ctx, t.Client.MCP, session, rt.Cache)
	var notices []string
	notices = append(notices, stabilizeNotice)
	if notice := detectCrossDomainRedirect(requestedURL, currentHref); notice != "" {
		notices = append(notices, notice)
	}
	return registry.Result{Content: appendNotices(out, notices)}, nil
}

// buildNavigateArgs returns a copy of argsMap with waitUntil set to the given
// value (only if the caller did not already provide one).
func buildNavigateArgs(argsMap map[string]json.RawMessage, waitUntil string) json.RawMessage {
	merged := make(map[string]json.RawMessage, len(argsMap)+1)
	for k, v := range argsMap {
		merged[k] = v
	}
	if _, has := merged["waitUntil"]; !has {
		merged["waitUntil"] = json.RawMessage(`"` + waitUntil + `"`)
	}
	out, _ := json.Marshal(merged)
	return out
}

// isNavigationTimeout reports whether err is a Playwright navigation timeout,
// which occurs when waitUntil:"networkidle" cannot be satisfied (e.g. a page
// with continuous background polling). The page is typically still usable.
func isNavigationTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") && strings.Contains(msg, "navigat")
}

// detectCrossDomainRedirect returns a warning when the page ended up on a
// different hostname than the one that was requested. This is a generic signal
// that the server redirected the browser — commonly to a login / auth page —
// without any need to enumerate specific provider URLs. Auth redirects are not
// automatically fatal: many apps expect automation to continue on the auth host.
func detectCrossDomainRedirect(requestedURL, currentHref string) string {
	if requestedURL == "" || currentHref == "" {
		return ""
	}
	req, err1 := url.Parse(requestedURL)
	cur, err2 := url.Parse(currentHref)
	if err1 != nil || err2 != nil {
		return ""
	}
	reqHost := strings.ToLower(req.Hostname())
	curHost := strings.ToLower(cur.Hostname())
	if reqHost == "" || curHost == "" || reqHost == curHost {
		return ""
	}
	// Ignore about:blank — stabilizePage already handles that case.
	if cur.Scheme == "about" {
		return ""
	}
	return "[Warning] Navigation to " + reqHost + " redirected to a different host (" + curHost + "). " +
		"This typically means the server requires authentication or has moved the resource. " +
		"If the user asked you to log in and credentials or a reusable session are available, inspect the page with pw-snapshot and continue with any visible login fields. " +
		"If no interactive form appears after one targeted recovery attempt, report the current URL, visible state, and tab list."
}

func ensurePageStable(ctx context.Context, client *mcp.Client, session mcp.Session, cache *registry.RuntimeCache) (notice, currentHref string) {
	if state, ok := cachedPageState(cache, client); ok && state.Stable {
		return "", state.Href
	}
	return stabilizeAndCachePage(ctx, client, session, cache)
}

func stabilizeAndCachePage(ctx context.Context, client *mcp.Client, session mcp.Session, cache *registry.RuntimeCache) (notice, currentHref string) {
	notice, currentHref = stabilizePage(ctx, client, session)
	setPageState(cache, client, pageState{
		Stable: notice == "" && currentHref != "",
		Href:   currentHref,
	})
	return notice, currentHref
}

func cachedPageState(cache *registry.RuntimeCache, client *mcp.Client) (pageState, bool) {
	if cache == nil || client == nil {
		return pageState{}, false
	}
	value, ok := cache.Get(pageStateCacheKey(client))
	if !ok {
		return pageState{}, false
	}
	state, ok := value.(pageState)
	return state, ok
}

func setPageState(cache *registry.RuntimeCache, client *mcp.Client, state pageState) {
	if cache == nil || client == nil {
		return
	}
	cache.Set(pageStateCacheKey(client), state)
}

func markPageStateUnknown(cache *registry.RuntimeCache, client *mcp.Client) {
	setPageState(cache, client, pageState{})
}

func pageStateCacheKey(client *mcp.Client) string {
	if client == nil {
		return pageStateCacheKeyPrefix
	}
	return pageStateCacheKeyPrefix + client.SessionKey()
}

func applyStealthPatch(ctx context.Context, client *mcp.Client, session mcp.Session) {
	// Inject stealth patches via evaluate as a best-effort fallback for when the
	// Playwright MCP server was not started with --init-script /stealth.js.
	// This runs after page load so it may miss detection scripts that fire during
	// page initialization. For robust stealth, use --init-script in the Docker command.
	_, _ = client.CallTool(ctx, session, "browser_evaluate", mustJSONRaw(map[string]any{
		"expression": stealthPatchExpr,
	}))
}

// stabilizePage runs post-navigation housekeeping. It returns the notice (may
// be empty) and the page's current href (extracted from the JS status payload,
// empty on error). Both values are used by the caller for redirect detection.
func stabilizePage(ctx context.Context, client *mcp.Client, session mcp.Session) (notice, currentHref string) {
	statusOut, evalErr := client.CallTool(ctx, session, "browser_evaluate", mustJSONRaw(map[string]any{
		"expression": pageStatusExpr,
	}))
	if evalErr != nil {
		return "", ""
	}

	// Extract href from the JSON payload returned by pageStatusExpr.
	var status struct {
		Href string `json:"href"`
	}
	if idx := strings.Index(statusOut, "{"); idx >= 0 {
		_ = json.Unmarshal([]byte(statusOut[idx:]), &status)
	}
	currentHref = status.Href

	if !strings.Contains(statusOut, "about:blank") {
		return "", currentHref
	}

	tabsOut, listErr := client.CallTool(ctx, session, "browser_tabs", json.RawMessage(`{"action":"list"}`))
	if listErr != nil {
		return "[Warning] Active page is about:blank and open tabs could not be listed. Use pw-get_all_pages to inspect browser state.", ""
	}
	tabIdx := findMainTabIndex(tabsOut)
	if tabIdx < 0 {
		return "[Warning] Active page is about:blank and no non-blank page tab was found. Use pw-get_all_pages to inspect browser state.", ""
	}
	_, _ = client.CallTool(ctx, session, "browser_tabs", mustJSONRaw(map[string]any{
		"action": "select",
		"index":  tabIdx,
	}))
	return "[Auto-recovery] Active page was about:blank, so focus was switched back to the main tab (index " + strconv.Itoa(tabIdx) + "). Call pw-snapshot to see the current page state.", ""
}

// findMainTabIndex parses browser_tabs list output and returns the index of the
// first tab whose URL is not about:blank. Returns -1 if none found.
//
// Expected line format from Playwright MCP:
//
//   - 0: (current) [Page Title](https://example.com/)
//   - 1: [Other Page](https://other.example.com/)
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
	// pw-snapshot uses a custom SnapshotTool for auth-page detection.
	reg.Register(SnapshotTool{Client: client})
	for _, tool := range tools(client) {
		reg.Register(tool)
	}
}

// RegisterDeferredAll registers Playwright tools as deferred until the browser
// category is activated. It is a no-op when the client is not configured.
func RegisterDeferredAll(reg *registry.Registry, client *Client, category string) {
	if client == nil || !client.enabled() {
		return
	}
	reg.RegisterDeferred(registry.AsDeferred(category, NavigateTool{Client: client}))
	reg.RegisterDeferred(registry.AsDeferred(category, SnapshotTool{Client: client}))
	for _, tool := range tools(client) {
		reg.RegisterDeferred(registry.AsDeferred(category, tool))
	}
}

// SnapshotTool wraps browser_snapshot with automatic auth-page detection.
// After the page stabilizes it inspects the current URL; if it looks like an
// auth/login redirect it injects the same cross-domain warning that NavigateTool
// emits, giving the model a second opportunity to stop and report to the user.
type SnapshotTool struct {
	Client *Client
}

func (t SnapshotTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec("pw-snapshot", "", registry.ObjectSchema(nil, map[string]any{}))
}

func (t SnapshotTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Playwright MCP is not configured: PLAYWRIGHT_MCP_URL is required")
	}
	session, err := getOrCreateSession(ctx, t.Client.MCP, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}

	// Retrieve the last URL the agent explicitly navigated to (set by NavigateTool).
	// This lets us detect cross-domain redirects that happened since the last
	// navigation, using the same host-comparison logic as NavigateTool so the
	// check is not tied to any site-specific URL patterns.
	var lastNavigatedURL string
	if rt.Cache != nil {
		if v, ok := rt.Cache.Get(lastNavigatedURLCacheKey); ok {
			lastNavigatedURL, _ = v.(string)
		}
	}

	var notices []string
	stabilizeNotice, currentHref := ensurePageStable(ctx, t.Client.MCP, session, rt.Cache)
	if stabilizeNotice != "" {
		notices = append(notices, stabilizeNotice)
	} else if redirectNotice := detectCrossDomainRedirect(lastNavigatedURL, currentHref); redirectNotice != "" {
		// Cross-domain redirect was missed by NavigateTool (JS fired after networkidle).
		// Augment the standard warning with login-form guidance.
		notices = append(notices, redirectNotice+
			" If a login form is visible, fill all required fields shown by the form before submitting.")
	}

	out, _, err := callToolWithRetry(ctx, t.Client.MCP, rt.Cache, session, "browser_snapshot", raw)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: appendNotices(sanitizeSnapshotOutput(out), notices)}, nil
}

func tools(client *Client) []MCPTool {
	// Note: pw-navigate and pw-snapshot are registered separately as custom
	// tool types (NavigateTool, SnapshotTool) and must NOT appear here.
	return []MCPTool{
		{
			Client:         client,
			LocalName:      "pw-click",
			RemoteName:     "browser_click",
			StabilizeAfter: true,
			Parameters: registry.ObjectSchema([]string{"ref"}, map[string]any{
				"ref":     map[string]any{"type": "string", "description": "Element ref from pw-snapshot (e.g. s1e4)."},
				"target":  map[string]any{"type": "string", "description": "Deprecated alias for ref; prefer ref."},
				"element": map[string]any{"type": "string", "description": "Human-readable description of the element, for logging."},
			}),
		},
		{
			Client:         client,
			LocalName:      "pw-type",
			RemoteName:     "browser_type",
			StabilizeAfter: true,
			Parameters: registry.ObjectSchema([]string{"ref", "text"}, map[string]any{
				"ref":     map[string]any{"type": "string", "description": "Element ref from pw-snapshot (e.g. s1e4)."},
				"target":  map[string]any{"type": "string", "description": "Deprecated alias for ref; prefer ref."},
				"element": map[string]any{"type": "string", "description": "Human-readable description of the element, for logging."},
				"text":    map[string]any{"type": "string", "description": "Text to type into the element."},
			}),
		},
		{
			Client:         client,
			LocalName:      "pw-fill_form",
			RemoteName:     "browser_fill_form",
			StabilizeAfter: true,
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
			Client:          client,
			LocalName:       "pw-screenshot",
			RemoteName:      "browser_take_screenshot",
			StabilizeBefore: true,
			// "type" is optional; omitting it defaults to PNG on the server side.
			Parameters: registry.ObjectSchema(nil, map[string]any{
				"type":     map[string]any{"type": "string", "enum": []string{"png", "jpeg"}, "description": "Image format. Defaults to png."},
				"fullPage": map[string]any{"type": "boolean", "description": "Capture the full scrollable page instead of just the viewport."},
				"filename": map[string]any{"type": "string", "description": "Optional filename hint for the screenshot."},
			}),
		},
		{
			Client:         client,
			LocalName:      "pw-press_key",
			RemoteName:     "browser_press_key",
			StabilizeAfter: true,
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
			Client:          client,
			LocalName:       "pw-evaluate",
			RemoteName:      "browser_evaluate",
			StabilizeBefore: true,
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
			Client:         client,
			LocalName:      "pw-switch_page",
			RemoteName:     "browser_tabs",
			StabilizeAfter: true,
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

// stealthPatchExpr is a compact JavaScript expression injected via browser_evaluate
// after each navigation. It suppresses common headless-browser detection signals as a
// best-effort fallback when the MCP server was not started with --init-script /stealth.js.
//
// Note: because browser_evaluate runs after the page has loaded, detection scripts that
// check navigator.webdriver during page initialization will have already fired. For full
// stealth coverage mount scripts/playwright-stealth.js via --init-script in Docker.
//
// The expression is a single-line IIFE so it can be safely embedded in a JSON string.
// All inner strings use single quotes; the outer JSON wrapper uses double quotes.
const stealthPatchExpr = `(function(){` +
	`try{Object.defineProperty(navigator,'webdriver',{get:()=>undefined,configurable:true})}catch(_){}` +
	`try{if(!window.chrome||!window.chrome.runtime){` +
	`window.chrome={app:{isInstalled:false},runtime:{id:undefined,connect:function(){},sendMessage:function(){}},` +
	`csi:function(){},loadTimes:function(){}};}}catch(_){}` +
	`try{var ua=navigator.userAgent;` +
	`if(ua.indexOf('HeadlessChrome')>=0){` +
	`Object.defineProperty(navigator,'userAgent',{get:()=>ua.replace(/HeadlessChrome\/[\d.]+ ?/g,''),configurable:true});` +
	`}}catch(_){}` +
	`})()`

const pageStatusExpr = `(async function(){` +
	`var wait=function(ms){return new Promise(function(r){setTimeout(r,ms)})};` +
	`var started=Date.now();` +
	`while(document.readyState==='loading'&&Date.now()-started<8000){await wait(100)}` +
	`var prev=window.location.href;` +
	`for(var i=0;i<15;i++){` +
	`await wait(200);` +
	`var curr=window.location.href;` +
	`if(curr===prev&&document.readyState!=='loading'){break;}` +
	`prev=curr;` +
	`}` +
	`return JSON.stringify({href:window.location.href,title:document.title,readyState:document.readyState});` +
	`})()`
