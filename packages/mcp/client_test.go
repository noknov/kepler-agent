package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc adapts a function to http.RoundTripper.
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

// TestHTTPClientReuse verifies that the lazily-initialized shared HTTP client
// is the same instance across calls (no per-RPC allocation).
func TestHTTPClientReuse(t *testing.T) {
	c := &Client{ServiceName: "test", URL: "http://example.test"}
	a := c.httpClient()
	b := c.httpClient()
	if a != b {
		t.Fatal("httpClient() returned different instances; shared client is not being reused")
	}
}

// TestHTTPClientInjection verifies that an injected HTTP client takes priority.
func TestHTTPClientInjection(t *testing.T) {
	injected := &http.Client{}
	c := &Client{HTTP: injected}
	if got := c.httpClient(); got != injected {
		t.Fatal("injected HTTP client was not returned")
	}
}

func TestFormatToolResult_TextOnly(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"text","text":"hello world"}]}`)
	got := FormatToolResult(raw)
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatToolResult_Image(t *testing.T) {
	// "iVBOR..." is the start of a valid PNG base64 — use a short valid base64 string.
	b64 := "aGVsbG8=" // base64("hello")
	raw := json.RawMessage(`{"content":[{"type":"image","data":"` + b64 + `","mimeType":"image/png"}]}`)
	got := FormatToolResult(raw)
	want := "data:image/png;base64," + b64
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatToolResult_ImageDefaultsMimeType(t *testing.T) {
	b64 := "aGVsbG8="
	raw := json.RawMessage(`{"content":[{"type":"image","data":"` + b64 + `"}]}`)
	got := FormatToolResult(raw)
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected image/png default mime type, got %q", got)
	}
}

func TestFormatToolResult_ImageInvalidBase64(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"image","data":"!!!not-base64!!!"}]}`)
	got := FormatToolResult(raw)
	if !strings.Contains(got, "invalid base64") {
		t.Fatalf("expected invalid base64 message, got %q", got)
	}
}

func TestFormatToolResult_MixedTextAndImage(t *testing.T) {
	b64 := "aGVsbG8="
	raw := json.RawMessage(`{"content":[
		{"type":"text","text":"caption"},
		{"type":"image","data":"` + b64 + `","mimeType":"image/jpeg"}
	]}`)
	got := FormatToolResult(raw)
	if !strings.Contains(got, "caption") {
		t.Fatalf("missing text part: %q", got)
	}
	if !strings.Contains(got, "data:image/jpeg;base64,"+b64) {
		t.Fatalf("missing image data URI: %q", got)
	}
}

func TestFormatToolResult_IsError(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"text","text":"boom"}],"isError":true}`)
	got := FormatToolResult(raw)
	if !strings.HasPrefix(got, "[tool error] ") {
		t.Fatalf("expected [tool error] prefix, got %q", got)
	}
}

func TestFormatToolResult_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not json`)
	got := FormatToolResult(raw)
	// Falls back to raw string.
	if got != "not json" {
		t.Fatalf("got %q", got)
	}
}

func TestInitialize_PropagatesNotifyError(t *testing.T) {
	callCount := 0
	c := &Client{
		ServiceName: "test",
		URL:         "http://example.test",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct{ Method string }
			_ = json.NewDecoder(r.Body).Decode(&payload)
			callCount++
			switch payload.Method {
			case "initialize":
				return httpResp(http.StatusOK,
					map[string]string{"Content-Type": "application/json", "Mcp-Session-Id": "s1"},
					`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`), nil
			case "notifications/initialized":
				// Server rejects the notification.
				return httpResp(http.StatusInternalServerError, nil, "internal error"), nil
			default:
				t.Fatalf("unexpected method %q", payload.Method)
				return nil, nil
			}
		})},
	}
	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when notifications/initialized fails, got nil")
	}
	if !strings.Contains(err.Error(), "notifications/initialized") {
		t.Fatalf("error should mention notifications/initialized: %v", err)
	}
}

func TestListToolsFollowsCursor(t *testing.T) {
	calls := 0
	c := &Client{ServiceName: "test", URL: "http://example.test", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		calls++
		if calls == 1 {
			return httpResp(200, map[string]string{"Content-Type": "application/json"}, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"one","inputSchema":{"type":"object"}}],"nextCursor":"next"}}`), nil
		}
		if payload.Params["cursor"] != "next" {
			t.Fatalf("params=%v", payload.Params)
		}
		return httpResp(200, map[string]string{"Content-Type": "application/json"}, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"two","inputSchema":{"type":"object"}}]}}`), nil
	})}}
	tools, err := c.ListTools(context.Background(), Session{ID: "s1", Initialized: true})
	if err != nil || len(tools) != 2 || tools[1].Name != "two" {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
}
