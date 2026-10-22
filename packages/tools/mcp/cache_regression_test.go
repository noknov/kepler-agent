package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestRemoteSessionsFollowCredentialRotationAndExpiry(t *testing.T) {
	var initialized atomic.Int32
	client := &mcp.Client{URL: "https://rotation.test", Token: "initial", HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		result := `{}`
		if payload.Method == "initialize" {
			initialized.Add(1)
			result = `{"protocolVersion":"2025-03-26","capabilities":{}}`
		}
		if payload.Method == "notifications/initialized" {
			return response(202, ``), nil
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": json.RawMessage(result)})
		res := response(200, string(data))
		res.Header.Set("Mcp-Session-Id", request.Header.Get("Authorization"))
		return res, nil
	})}}
	state := &serverState{sessions: make(map[string]sessionEntry), initializing: make(map[string]*sessionInitialization)}
	call := tool.Call{Scope: tool.Scope{SessionID: "s", UserID: "u", Values: map[string]string{"team_id": "team1"}}}
	old, err := state.session(context.Background(), call, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.session(context.Background(), call, client); err != nil {
		t.Fatal(err)
	}
	client.Token = "rotated"
	next, err := state.session(context.Background(), call, client)
	if err != nil {
		t.Fatal(err)
	}
	if old == next || initialized.Load() != 2 {
		t.Fatalf("old=%+v new=%+v handshakes=%d", old, next, initialized.Load())
	}
	// A late failure using the old token must not evict the new session.
	oldClient := &mcp.Client{URL: client.URL, Token: "initial", HTTP: client.HTTP}
	state.invalidateSession(call, oldClient, old)
	if _, err = state.session(context.Background(), call, client); err != nil {
		t.Fatal(err)
	}
	if initialized.Load() != 2 {
		t.Fatal("old failure invalidated current credentials")
	}
	key := state.sessionKey(call, client)
	entry := state.sessions[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	state.sessions[key] = entry
	if _, err = state.session(context.Background(), call, client); err != nil {
		t.Fatal(err)
	}
	call.Scope.Values["team_id"] = "team2"
	if _, err = state.session(context.Background(), call, client); err != nil {
		t.Fatal(err)
	}
	if initialized.Load() != 4 {
		t.Fatalf("expiry/tenant did not reinitialize: %d", initialized.Load())
	}
	if strings.Contains(routerCacheKey(client), "rotated") {
		t.Fatal("credential retained in cache key")
	}
}
