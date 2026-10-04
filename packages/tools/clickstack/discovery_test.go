package clickstack

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	agentmcp "github.com/noknov/kepler-agent/packages/mcp"
)

// Inventory must not refresh credentials or discover remote schemas. Either
// operation can fail independently of whether the saved instance exists.
type inventoryStore struct{ connections.Store }

type inventoryModel func(context.Context, model.Request, model.EventSink) (model.Response, error)

func (m inventoryModel) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	return m(ctx, request, sink)
}

func (inventoryStore) RawToken(context.Context, string, string) (string, error) {
	panic("inventory read credentials")
}

func TestConnectionInventoryIncludesUnnamedAndNamedInstances(t *testing.T) {
	ctx := context.Background()
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ user, id, label string }{{"U1", "default", ""}, {"U1", "i-eu", "clickstack-eu"}, {"U2", "i-private", "Other user's connection"}} {
		if err := store.UpsertTokenInstance(ctx, item.user, connections.ProviderClickStack, item.id, item.label, "invalid-token", nil, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (connectionListTool{conn: &connections.Service{Store: inventoryStore{store}}, cfg: config.ClickStackConfig{ServiceID: "legacy-service"}}).Execute(ctx, tool.Call{Scope: tool.Scope{UserID: "U1"}})
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Total       int                 `json:"total"`
		Connections []connectionSummary `json:"connections"`
	}
	if err := json.Unmarshal([]byte(result.Text()), &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 2 || len(inventory.Connections) != 2 {
		t.Fatalf("inventory lost a connection: %s", result.Text())
	}
	first, second := inventory.Connections[0], inventory.Connections[1]
	if first.InstanceID != "default" || first.Label != "" || first.DisplayName != "ClickStack" || second.InstanceID != "i-eu" || second.Label != "clickstack-eu" {
		t.Fatalf("inventory changed instance identity or name: %s", result.Text())
	}
	for _, item := range inventory.Connections {
		if item.ServiceID != "legacy-service" || item.TargetOrigin != "deployment_config" {
			t.Fatalf("legacy target is ambiguous: %+v", item)
		}
	}
	if len(result.Text()) > 1024 || strings.Contains(result.Text(), "inputSchema") || strings.Contains(result.Text(), "Other user's") {
		t.Fatalf("inventory is not compact and user scoped: %s", result.Text())
	}
	t.Logf("two-connection inventory: %d bytes", len(result.Text()))

	// Verify the runtime actually delivers both instances in the next model
	// request, without requiring the model to page through an artifact.
	steps := 0
	client := inventoryModel(func(_ context.Context, request model.Request, _ model.EventSink) (model.Response, error) {
		steps++
		if steps == 1 {
			call := model.ToolCall{ID: "inventory", Name: "clickstack_connections", Arguments: json.RawMessage(`{}`)}
			return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &call}}}, FinishReason: model.FinishToolCalls}, nil
		}
		found := false
		for _, message := range request.Messages {
			for _, block := range message.Content {
				if block.ToolResult == nil || block.ToolResult.Name != "clickstack_connections" {
					continue
				}
				var body string
				for _, content := range block.ToolResult.Content {
					if content.Type == model.ContentArtifact {
						t.Fatal("connection inventory spilled out of model context")
					}
					body += content.Text
				}
				var projected struct {
					Total       int                 `json:"total"`
					Connections []connectionSummary `json:"connections"`
				}
				if err := json.Unmarshal([]byte(body), &projected); err != nil || projected.Total != 2 || len(projected.Connections) != 2 || projected.Connections[0].InstanceID != "default" || projected.Connections[1].InstanceID != "i-eu" {
					t.Fatalf("LLM did not receive both independent instances: %s, err=%v", body, err)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("connection inventory never reached the model request")
		}
		return model.Response{Message: model.TextMessage(model.RoleAssistant, "Both connections are visible."), FinishReason: model.FinishStop}, nil
	})
	catalog, err := tool.NewCatalog(connectionListTool{conn: &connections.Service{Store: inventoryStore{store}}, cfg: config.ClickStackConfig{ServiceID: "legacy-service"}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agentruntime.New(agentruntime.Config{Model: "test", ToolResults: agentruntime.ToolResultConfig{MaxInlineBytes: 2048}}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: transcript.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(ctx, agentruntime.TurnRequest{SessionID: "inventory-context", Input: model.TextMessage(model.RoleUser, "List my integrations"), Scope: tool.Scope{UserID: "U1"}}); err != nil {
		t.Fatal(err)
	}
	if steps != 2 {
		t.Fatalf("model steps=%d, want inventory then answer", steps)
	}
}

func TestToolsDiscoveryPagesNamesAndReturnsOnlySelectedSchema(t *testing.T) {
	ctx := context.Background()
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"i-prod", "i-eu"} {
		if err := store.UpsertTokenInstance(ctx, "U1", connections.ProviderClickStack, id, "", id+"-token", nil, "", map[string]string{"mcp_url": "https://example.test/mcp", "service_id": id}); err != nil {
			t.Fatal(err)
		}
	}
	readonly := true
	definitions := []agentmcp.ToolDefinition{}
	for n := 0; n < 33; n++ {
		definitions = append(definitions, agentmcp.ToolDefinition{Name: fmt.Sprintf("tool_%02d", n), Description: strings.Repeat("long remote instructions ", 500), InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`), Annotations: &agentmcp.ToolAnnotations{ReadOnlyHint: &readonly}})
	}
	seen := []string{}
	factory := func(connection connections.Connection, token string) *agentmcp.Client {
		if token != connection.InstanceID+"-token" {
			t.Errorf("wrong token for %s", connection.InstanceID)
		}
		resolved := configForConnection(config.ClickStackConfig{ServiceID: "operator-service", TeamID: "operator-team"}, connection)
		if resolved.ServiceID != connection.InstanceID || resolved.TeamID != "" {
			t.Errorf("routing crossed connections: %+v", resolved)
		}
		seen = append(seen, connection.InstanceID)
		return &agentmcp.Client{ServiceName: "test", URL: "https://example.test/mcp", Token: token, HTTP: &http.Client{Transport: clickStackRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			var message struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
				return nil, err
			}
			var value any = map[string]any{}
			switch message.Method {
			case "initialize":
				value = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}
			case "notifications/initialized":
				return clickStackResponse(http.StatusAccepted, ""), nil
			case "tools/list":
				value = map[string]any{"tools": definitions}
			default:
				return nil, fmt.Errorf("unexpected method %s", message.Method)
			}
			response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": value})
			return clickStackResponse(http.StatusOK, string(response)), err
		})}}
	}
	discovery := connectionToolsTool{connectionCallTool{conn: &connections.Service{Store: store}, clientFactory: factory}}
	run := func(args string, user string) tool.Result {
		t.Helper()
		result, err := discovery.Execute(ctx, tool.Call{Arguments: json.RawMessage(args), Scope: tool.Scope{UserID: user}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	page := run(`{"instance_id":"i-eu","limit":2,"offset":1}`, "U1")
	var catalog struct {
		Total int `json:"total"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		NextOffset int `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(page.Text()), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Total != 33 || len(catalog.Tools) != 2 || catalog.Tools[0].Name != "tool_01" || catalog.NextOffset != 3 || strings.Contains(page.Text(), "inputSchema") || len(page.Text()) > 512 {
		t.Fatalf("catalog did not page compact names: %s", page.Text())
	}
	schema := run(`{"instance_id":"i-prod","tool_name":"tool_32"}`, "U1")
	if !strings.Contains(schema.Text(), `"inputSchema"`) || !strings.Contains(schema.Text(), `"tool_32"`) || strings.Contains(schema.Text(), `"tool_00"`) {
		t.Fatalf("wrong schema selection: %s", schema.Text())
	}
	missing := run(`{"instance_id":"i-eu","tool_name":"missing"}`, "U1")
	if !missing.IsError || missing.ErrorCode != "remote_tool_not_found" {
		t.Fatalf("missing tool: %+v", missing)
	}
	crossUser := run(`{"instance_id":"i-eu"}`, "U2")
	if !crossUser.IsError || crossUser.ErrorCode != "connection_required" || len(seen) != 3 {
		t.Fatalf("cross-user discovery reached remote server: %+v, seen=%v", crossUser, seen)
	}
	for _, args := range []string{`{"instance_id":"i-eu","limit":0}`, `{"instance_id":"i-eu","offset":-1}`} {
		if _, err := discovery.Execute(ctx, tool.Call{Arguments: json.RawMessage(args), Scope: tool.Scope{UserID: "U1"}}); err == nil {
			t.Fatal("invalid pagination accepted")
		}
	}
	beyond := run(fmt.Sprintf(`{"instance_id":"i-eu","offset":%d}`, math.MaxInt), "U1")
	if strings.Contains(beyond.Text(), "next_offset") || !strings.Contains(beyond.Text(), `"tools":[]`) {
		t.Fatalf("large offset overflowed: %s", beyond.Text())
	}
}
