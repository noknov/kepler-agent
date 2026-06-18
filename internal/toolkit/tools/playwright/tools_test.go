package playwright

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/mcp"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpResp(status int, headers map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// mcpTransport returns an http.RoundTripper that handles initialize + notifications/initialized
// and delegates tools/call to the provided handler.
func mcpTransport(t *testing.T, sessionID string, toolHandler func(name string) string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		switch payload.Method {
		case "initialize":
			return httpResp(http.StatusOK,
				map[string]string{"Content-Type": "application/json", "Mcp-Session-Id": sessionID},
				`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`), nil
		case "notifications/initialized":
			return httpResp(http.StatusAccepted, nil, ""), nil
		case "tools/call":
			var params struct{ Name string }
			_ = json.Unmarshal(payload.Params, &params)
			result := toolHandler(params.Name)
			return httpResp(http.StatusOK,
				map[string]string{"Content-Type": "application/json"},
				`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"`+result+`"}]}}`), nil
		default:
			t.Fatalf("unexpected method %q", payload.Method)
			return nil, nil
		}
	})
}

func TestExecute_BasicNavigation(t *testing.T) {
	client := &Client{MCP: &mcp.Client{
		ServiceName: "playwright",
		URL:         "http://playwright.test/mcp",
		HTTP:        &http.Client{Transport: mcpTransport(t, "sess-1", func(name string) string { return "navigated" })},
	}}
	tool := MCPTool{Client: client, LocalName: "pw-navigate", RemoteName: "browser_navigate"}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "navigated" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestExecute_NotConfigured(t *testing.T) {
	tool := MCPTool{Client: &Client{MCP: &mcp.Client{URL: ""}}, LocalName: "pw-navigate"}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`), registry.Runtime{})
	if err == nil || !strings.Contains(err.Error(), "PLAYWRIGHT_MCP_URL") {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestExecute_SessionRetryOn404(t *testing.T) {
	callCount := 0
	initCount := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch payload.Method {
		case "initialize":
			initCount++
			return httpResp(http.StatusOK,
				map[string]string{"Content-Type": "application/json", "Mcp-Session-Id": "sess-new"},
				`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`), nil
		case "notifications/initialized":
			return httpResp(http.StatusAccepted, nil, ""), nil
		case "tools/call":
			callCount++
			if callCount == 1 {
				// Simulate session expired.
				return httpResp(http.StatusNotFound, nil, "session not found"), nil
			}
			return httpResp(http.StatusOK,
				map[string]string{"Content-Type": "application/json"},
				`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok after retry"}]}}`), nil
		default:
			return httpResp(http.StatusOK, nil, ""), nil
		}
	})

	client := &Client{MCP: &mcp.Client{
		ServiceName: "playwright",
		URL:         "http://playwright.test/mcp",
		HTTP:        &http.Client{Transport: transport},
	}}
	tool := MCPTool{Client: client, LocalName: "pw-snapshot", RemoteName: "browser_snapshot"}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ok after retry" {
		t.Fatalf("content = %q", result.Content)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 tool calls (1 expired + 1 retry), got %d", callCount)
	}
	if initCount != 2 {
		t.Fatalf("expected 2 initialize calls (initial + retry), got %d", initCount)
	}
}

func TestIsSessionExpired(t *testing.T) {
	cases := []struct {
		msg    string
		expect bool
	}{
		{"mcp playwright: status 404: session not found", true},
		{"mcp error -32001: session expired", true},
		{"Session not found", true},
		{"session not found", true},
		{"mcp playwright: status 500: internal server error", false},
		{"connection refused", false},
	}
	for _, tc := range cases {
		err := &errString{tc.msg}
		if got := isSessionExpired(err); got != tc.expect {
			t.Errorf("isSessionExpired(%q) = %v, want %v", tc.msg, got, tc.expect)
		}
	}
}

type errString struct{ s string }

func (e *errString) Error() string { return e.s }

func TestRegisterAll_SkipsWhenNotConfigured(t *testing.T) {
	reg := registry.New()
	RegisterAll(reg, &Client{MCP: &mcp.Client{URL: ""}})
	for _, name := range reg.Names() {
		if strings.HasPrefix(name, "pw-") {
			t.Fatalf("pw-* tool %q was registered despite empty URL", name)
		}
	}
}

func TestRegisterAll_RegistersWhenConfigured(t *testing.T) {
	reg := registry.New()
	RegisterAll(reg, &Client{MCP: &mcp.Client{URL: "http://localhost:8931/mcp"}})
	names := strings.Join(reg.Names(), "\n")
	for _, want := range []string{"pw-navigate", "pw-snapshot", "pw-click", "pw-screenshot"} {
		if !strings.Contains(names, want) {
			t.Errorf("expected tool %q to be registered, got:\n%s", want, names)
		}
	}
}

func TestExecute_ScreenshotStoresInCacheNotLLMContext(t *testing.T) {
	b64 := "aGVsbG8=" // base64("hello")
	dataURI := "data:image/png;base64," + b64

	// Playwright MCP returns text summary + image data URI concatenated.
	mcpResponse := `{"jsonrpc":"2.0","id":2,"result":{"content":[` +
		`{"type":"text","text":"### Result\n- [Screenshot of viewport](.playwright-mcp/page.png)"}` +
		`,{"type":"image","data":"` + b64 + `","mimeType":"image/png"}` +
		`]}}`

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch payload.Method {
		case "initialize":
			return httpResp(http.StatusOK,
				map[string]string{"Content-Type": "application/json", "Mcp-Session-Id": "s1"},
				`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`), nil
		case "notifications/initialized":
			return httpResp(http.StatusAccepted, nil, ""), nil
		case "tools/call":
			return httpResp(http.StatusOK,
				map[string]string{"Content-Type": "application/json"},
				mcpResponse), nil
		default:
			t.Fatalf("unexpected method %q", payload.Method)
			return nil, nil
		}
	})

	client := &Client{MCP: &mcp.Client{
		ServiceName: "playwright",
		URL:         "http://playwright.test/mcp",
		HTTP:        &http.Client{Transport: transport},
	}}
	tool := MCPTool{Client: client, LocalName: "pw-screenshot", RemoteName: "browser_take_screenshot"}

	cache := registry.NewRuntimeCache()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), registry.Runtime{Cache: cache})
	if err != nil {
		t.Fatal(err)
	}

	// LLM should see only a short text, NOT the base64 data URI.
	if strings.Contains(result.Content, "data:image/") {
		t.Fatalf("data URI leaked into LLM result: %q", result.Content[:min(200, len(result.Content))])
	}
	if !strings.Contains(result.Content, "slack-send_screenshot") {
		t.Fatalf("result should mention slack-send_screenshot, got: %q", result.Content)
	}

	// Cache must contain the data URI for slack-send_screenshot to pick up.
	cached, ok := cache.Get(ScreenshotCacheKey)
	if !ok {
		t.Fatal("screenshot was not stored in cache")
	}
	if cached.(string) != dataURI {
		t.Fatalf("cached value = %q, want %q", cached, dataURI)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestExtractDataURI(t *testing.T) {
	b64 := "aGVsbG8="
	uri := "data:image/png;base64," + b64

	cases := []struct {
		input       string
		wantURI     string
		wantHasRest bool
	}{
		// URI alone
		{uri, uri, false},
		// Text before URI (typical Playwright MCP format)
		{"### Result\n- [Screenshot](file.png)\n" + uri + "\n", uri, true},
		// No URI
		{"just text", "", false},
		// URI mid-string with trailing newline
		{"prefix\n" + uri + "\nmore text", uri, true},
	}
	for _, tc := range cases {
		gotURI, gotRest := extractDataURI(tc.input)
		if gotURI != tc.wantURI {
			t.Errorf("extractDataURI(%q): URI = %q, want %q", tc.input[:min(40, len(tc.input))], gotURI, tc.wantURI)
		}
		if tc.wantHasRest && strings.TrimSpace(gotRest) == "" {
			t.Errorf("extractDataURI(%q): expected non-empty rest", tc.input[:min(40, len(tc.input))])
		}
		if !tc.wantHasRest && gotURI == "" && gotRest != tc.input {
			t.Errorf("extractDataURI(%q): no URI case should return original string as rest", tc.input)
		}
	}
}

func TestScreenshotSchemaTypeNotRequired(t *testing.T) {
	client := &Client{MCP: &mcp.Client{URL: "http://localhost:8931/mcp"}}
	for _, tool := range tools(client) {
		if tool.LocalName != "pw-screenshot" {
			continue
		}
		schema, ok := tool.Parameters["required"]
		if !ok {
			return // no required field at all — correct
		}
		required, ok := schema.([]string)
		if !ok {
			return
		}
		for _, r := range required {
			if r == "type" {
				t.Fatal("pw-screenshot schema has 'type' in required; it should be optional")
			}
		}
		return
	}
	t.Fatal("pw-screenshot tool not found")
}
