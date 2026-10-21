package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
)

func TestRouterKeyAndConcurrentDiscoveryAreStable(t *testing.T) {
	var initialized atomic.Int32
	client := &mcp.Client{URL: "https://cache.test", Token: "private", Headers: map[string]string{"X-Tenant": "a", "X-Region": "b"}, HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		result := `{}`
		switch payload.Method {
		case "initialize":
			initialized.Add(1)
			result = `{"protocolVersion":"2025-03-26","capabilities":{}}`
		case "notifications/initialized":
			return response(202, ``), nil
		case "tools/list":
			result = `{"tools":[]}`
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": json.RawMessage(result)})
		return response(200, string(data)), nil
	})}}
	key := routerCacheKey(client)
	for i := 0; i < 100; i++ {
		if routerCacheKey(client) != key {
			t.Fatal("cache key varies with header iteration")
		}
	}
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := ListDefinitions(context.Background(), client); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if initialized.Load() != 1 {
		t.Fatalf("initialized %d times", initialized.Load())
	}
}

func TestDiscoveryPreservesStaticTokenForExecution(t *testing.T) {
	client := &mcp.Client{URL: "https://auth.test", Token: "static-token", HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer static-token" {
			t.Errorf("missing authentication for %s", request.URL)
		}
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
			result = `{"tools":[{"name":"lookup","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"ok"}]}`
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": json.RawMessage(result)})
		return response(200, string(data)), nil
	})}}
	items, err := Discover(context.Background(), Server{Name: "auth", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items[0].Execute(context.Background(), tool.Call{Scope: tool.Scope{SessionID: "s"}, Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
}
