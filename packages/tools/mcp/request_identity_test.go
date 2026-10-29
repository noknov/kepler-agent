package mcptools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
)

func TestCachedSessionsDoNotReuseRequestIDs(t *testing.T) {
	for _, mode := range []string{"router", "discovered"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			seen := make(map[string]map[string]bool)
			sessions, calls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Method == "notifications/initialized" {
					if len(request.ID) != 0 {
						t.Error("notification has a request ID")
					}
					w.WriteHeader(http.StatusAccepted)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				sessionID := r.Header.Get("Mcp-Session-Id")
				if request.Method == "initialize" {
					sessions++
					sessionID = fmt.Sprintf("session-%d", sessions)
					w.Header().Set("Mcp-Session-Id", sessionID)
					seen[sessionID] = make(map[string]bool)
				}
				id := string(request.ID)
				if seen[sessionID] == nil || seen[sessionID][id] {
					http.Error(w, "request ID reused within session", http.StatusBadRequest)
					return
				}
				seen[sessionID][id] = true
				result := json.RawMessage(`{}`)
				switch request.Method {
				case "initialize":
					result = json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{}}`)
				case "tools/list":
					result = json.RawMessage(`{"tools":[{"name":"lookup","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}`)
				case "tools/call":
					calls++
					result = json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			defer server.Close()
			httpClient := server.Client()
			newClient := func() *mcp.Client { return &mcp.Client{URL: server.URL, HTTP: httpClient} }
			call := tool.Call{Scope: tool.Scope{SessionID: "local-session"}, Arguments: json.RawMessage(`{}`)}
			invoke := func() (tool.Result, error) { return InvokeReadOnly(t.Context(), newClient(), "lookup", call.Arguments) }
			if mode == "discovered" {
				items, err := Discover(t.Context(), Server{Name: "identity", Client: newClient()})
				if err != nil {
					t.Fatal(err)
				}
				invoke = func() (tool.Result, error) { return items[0].Execute(t.Context(), call) }
			}
			for range 2 {
				if _, err := invoke(); err != nil {
					t.Fatal(err)
				}
			}
			var wait sync.WaitGroup
			for range 8 {
				wait.Go(func() {
					if _, err := invoke(); err != nil {
						t.Error(err)
					}
				})
			}
			wait.Wait()
			mu.Lock()
			defer mu.Unlock()
			if calls != 10 || sessions != map[string]int{"router": 1, "discovered": 2}[mode] {
				t.Fatalf("calls=%d sessions=%d", calls, sessions)
			}
		})
	}
}
