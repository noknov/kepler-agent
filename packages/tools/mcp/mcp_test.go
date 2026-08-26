package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestDiscoverAndExecute(t *testing.T) {
	client := &mcp.Client{ServiceName: "demo", URL: "https://mcp.test", HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		result := `{}`
		switch payload.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-03-26","capabilities":{}}`
		case "notifications/initialized":
			return response(202, ``), nil
		case "tools/list":
			result = `{"tools":[{"name":"lookup","description":"Look up data.","inputSchema":{"type":"object"}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"found"}]}`
		default:
			t.Fatalf("method=%s", payload.Method)
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": json.RawMessage(result)})
		return response(200, string(data)), nil
	})}}
	items, err := Discover(context.Background(), Server{Name: "demo", Client: client, Effects: []agenttool.Effect{agenttool.EffectRead}})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if items[0].Descriptor().Name != "mcp_demo_lookup" || items[0].Descriptor().Exposure != agenttool.ExposureDeferred {
		t.Fatalf("descriptor=%+v", items[0].Descriptor())
	}
	result, err := items[0].Execute(context.Background(), agenttool.Call{Scope: agenttool.Scope{SessionID: "s1"}, Arguments: json.RawMessage(`{}`)})
	if err != nil || result.Text() != "found" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}, "Mcp-Session-Id": []string{"remote"}}, Body: io.NopCloser(bytes.NewBufferString(body))}
}
