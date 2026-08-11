package playwright

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

func TestIntegration_PlaywrightMCPRealBrowserSmoke(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("PLAYWRIGHT_MCP_URL"))
	if endpoint == "" {
		t.Skip("set PLAYWRIGHT_MCP_URL to run the real Playwright MCP browser smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	client := &Client{MCP: &mcp.Client{
		ServiceName: "playwright-integration",
		URL:         endpoint,
		Token:       os.Getenv("PLAYWRIGHT_MCP_TOKEN"),
		HTTP:        &http.Client{Timeout: 45 * time.Second},
	}}
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}

	html := `<!doctype html>
<html>
<head><title>Playwright smoke test</title></head>
<body>
  <label>Name <input aria-label="Name" id="name"></label>
  <button onclick="document.getElementById('result').textContent = 'done:' + document.getElementById('name').value">Save</button>
  <output id="result">ready</output>
</body>
</html>`
	pageURL := "data:text/html;charset=utf-8," + url.PathEscape(html)

	if _, err := (NavigateTool{Client: client}).Execute(ctx, mustJSONRaw(map[string]any{"url": pageURL}), rt); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	snapshotTool := MCPTool{Client: client, LocalName: "pw-snapshot", RemoteName: "browser_snapshot", StabilizeBefore: true}
	snapshot, err := snapshotTool.Execute(ctx, json.RawMessage(`{}`), rt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	inputRef := refFor(snapshot.Content, "Name")
	buttonRef := refFor(snapshot.Content, "Save")
	if inputRef == "" || buttonRef == "" {
		t.Fatalf("snapshot did not contain expected refs; input=%q button=%q\n%s", inputRef, buttonRef, snapshot.Content)
	}

	typeTool := MCPTool{Client: client, LocalName: "pw-type", RemoteName: "browser_type", StabilizeAfter: true}
	if _, err := typeTool.Execute(ctx, mustJSONRaw(map[string]any{
		"target":  inputRef, // Exercise backward-compatible target -> ref normalization.
		"element": "Name input",
		"text":    "Ada",
	}), rt); err != nil {
		t.Fatalf("type: %v", err)
	}

	clickTool := MCPTool{Client: client, LocalName: "pw-click", RemoteName: "browser_click", StabilizeAfter: true}
	if _, err := clickTool.Execute(ctx, mustJSONRaw(map[string]any{
		"ref":     buttonRef,
		"element": "Save button",
	}), rt); err != nil {
		t.Fatalf("click: %v", err)
	}

	evaluateTool := MCPTool{Client: client, LocalName: "pw-evaluate", RemoteName: "browser_evaluate", StabilizeBefore: true}
	evaluated, err := evaluateTool.Execute(ctx, mustJSONRaw(map[string]any{
		"expression": "document.getElementById('result').textContent",
	}), rt)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !strings.Contains(evaluated.Content, "done:Ada") {
		t.Fatalf("expected click/type result done:Ada, got:\n%s", evaluated.Content)
	}

	screenshotTool := MCPTool{Client: client, LocalName: "pw-screenshot", RemoteName: "browser_take_screenshot", StabilizeBefore: true}
	screenshot, err := screenshotTool.Execute(ctx, json.RawMessage(`{"fullPage":true}`), rt)
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if !strings.Contains(screenshot.Content, "Screenshot captured") {
		t.Fatalf("expected screenshot capture summary, got:\n%s", screenshot.Content)
	}
	if _, ok := rt.Cache.Get(ScreenshotCacheKey); !ok {
		t.Fatal("expected screenshot data URI to be cached")
	}
}

func refFor(snapshot, label string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(label) + `"[^\n]*\[ref=([^\]]+)\]`)
	if match := re.FindStringSubmatch(snapshot); len(match) == 2 {
		return match[1]
	}
	return ""
}
