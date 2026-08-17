package luckin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/mcp"
	agenttool "github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

func TestMCPToolCallsStreamableHTTP(t *testing.T) {
	var sessionID string
	var callCount int
	client := &Client{MCP: &mcp.Client{URL: "https://luckin.test/mcp", Token: "token", HTTP: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			var payload struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			switch payload.Method {
			case "initialize":
				sessionID = "session-123"
				return httpResponse(http.StatusOK, map[string]string{
					"Mcp-Session-Id": sessionID,
					"Content-Type":   "application/json",
				}, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`), nil
			case "notifications/initialized":
				if got := r.Header.Get("Mcp-Session-Id"); got != sessionID {
					t.Fatalf("initialized session = %q", got)
				}
				return httpResponse(http.StatusAccepted, nil, ""), nil
			case "tools/call":
				callCount++
				if got := r.Header.Get("Mcp-Session-Id"); got != sessionID {
					t.Fatalf("tool session = %q", got)
				}
				var params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				if err := json.Unmarshal(payload.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params.Name != "queryShopList" {
					t.Fatalf("tool name = %q", params.Name)
				}
				if params.Arguments["longitude"] != 118.08891 {
					t.Fatalf("arguments = %#v", params.Arguments)
				}
				return httpResponse(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"},
					"event: message\n"+`data: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}`+"\n\n"), nil
			default:
				t.Fatalf("unexpected method %q", payload.Method)
			}
			return nil, nil
		}),
	}}}
	mcpTool := MCPTool{
		Client:     client,
		LocalName:  "luckin-query_shop_list",
		RemoteName: "queryShopList",
		Parameters: agenttool.ObjectSchema([]string{"longitude", "latitude"}, map[string]any{
			"longitude": map[string]any{"type": "number"},
			"latitude":  map[string]any{"type": "number"},
		}),
	}

	result, err := mcpTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"longitude":118.08891,"latitude":24.479627}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text() != "ok" {
		t.Fatalf("content = %q", result.Text())
	}
	if callCount != 1 {
		t.Fatalf("call count = %d", callCount)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func httpResponse(status int, headers map[string]string, body string) *http.Response {
	h := http.Header{}
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
