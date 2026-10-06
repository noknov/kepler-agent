package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/profiles/local"
	localtools "github.com/noknov/kepler-agent/packages/tools/local"
)

func TestConfiguredMCPToolsAreDiscoverableAndExecutable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = json.RawMessage(`{"tools":[{"name":"lookup","description":"Look up a record","inputSchema":{"type":"object"}}]}`)
		case "tools/call":
			result = json.RawMessage(`{"content":[{"type":"text","text":"record found"}]}`)
		default:
			t.Errorf("unexpected MCP method: %s", request.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	workspace, err := local.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	catalog, err := localtools.NewCatalog(workspace, local.Sandbox{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	config := local.DefaultConfig()
	config.MCPServers = []local.MCPServerConfig{{Name: "demo", URL: server.URL}}
	ctx := context.Background()
	if _, err := registerSkillsAndMCP(ctx, catalog, workspace, config); err != nil {
		t.Fatal(err)
	}
	scope := tool.Scope{SessionID: "session", TurnID: "turn"}
	if _, ok := catalog.GetActive(scope.SessionID, "mcp_demo_lookup"); ok {
		t.Fatal("MCP tool loaded before explicit activation")
	}
	search, ok := catalog.GetActive(scope.SessionID, "tool_search")
	if !ok {
		t.Fatal("configured MCP tools have no model-visible discovery entry")
	}
	listed, err := search.Execute(ctx, tool.Call{Scope: scope, Arguments: json.RawMessage(`{"action":"list"}`)})
	if err != nil || !strings.Contains(listed.Text(), "demo") {
		t.Fatalf("MCP server missing from deferred categories: result=%+v err=%v", listed, err)
	}
	result, err := search.Execute(ctx, tool.Call{Scope: scope, Arguments: json.RawMessage(`{"action":"search","query":"lookup"}`)})
	if err != nil || !strings.Contains(result.Text(), "mcp_demo_lookup") {
		t.Fatalf("search result=%+v err=%v", result, err)
	}
	if _, err := search.Execute(ctx, tool.Call{Scope: scope, Arguments: json.RawMessage(`{"action":"activate","tool_names":["mcp_demo_lookup"]}`)}); err != nil {
		t.Fatal(err)
	}
	remote, ok := catalog.GetActive(scope.SessionID, "mcp_demo_lookup")
	if !ok {
		t.Fatal("discovered MCP tool could not be activated")
	}
	result, err = remote.Execute(ctx, tool.Call{Scope: scope, Arguments: json.RawMessage(`{}`)})
	if err != nil || result.Text() != "record found" {
		t.Fatalf("remote result=%+v err=%v", result, err)
	}
}
