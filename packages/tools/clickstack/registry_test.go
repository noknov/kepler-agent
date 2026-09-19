package clickstack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	agentmcp "github.com/noknov/kepler-agent/packages/mcp"
)

type clickStackRoundTripFunc func(*http.Request) (*http.Response, error)

func (f clickStackRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestInstanceResolverKeepsClickStackCredentialsSeparate(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertTokenInstance(ctx, "U1", connections.ProviderClickStack, "i-prod", "Production", "prod-token", nil, "", map[string]string{"mcp_url": "https://prod.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTokenInstance(ctx, "U1", connections.ProviderClickStack, "i-eu", "Europe", "eu-token", nil, "", map[string]string{"mcp_url": "https://eu.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	r := &Registrar{conn: &connections.Service{Store: store}}
	for _, want := range []struct{ instance, token string }{{"i-prod", "prod-token"}, {"i-eu", "eu-token"}} {
		got, err := r.tokenResolverForInstance(want.instance)(ctx, tool.Call{Scope: tool.Scope{UserID: "U1"}})
		if err != nil || got != want.token {
			t.Fatalf("resolver(%q) = (%q, %v), want %q", want.instance, got, err, want.token)
		}
	}
}

func TestConfigForConnectionUsesItsMCPURLAndDisplayHint(t *testing.T) {
	r := &Registrar{cfg: config.ClickStackConfig{MCPURL: "https://default.example/mcp"}}
	connection := connections.Connection{
		Provider: connections.ProviderClickStack, InstanceID: "i-eu", Label: "Europe logs",
		Metadata: map[string]string{"mcp_url": "https://eu.example/mcp"},
	}
	if got := r.configForConnection(connection).MCPURL; got != "https://eu.example/mcp" {
		t.Fatalf("configForConnection() MCPURL = %q", got)
	}
	if got := connections.InstanceDisplayName(connection); got != "Europe logs" {
		t.Fatalf("InstanceDisplayName() = %q", got)
	}
}

func TestRegistrarCatalogDoesNotExposeOneUsersInstances(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTokenInstance(context.Background(), "user-a", connections.ProviderClickStack, "i-a", "A private server", "a-token", nil, "", map[string]string{"mcp_url": "https://a.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	service := &connections.Service{Store: store, Config: connections.Config{PublicBaseURL: "https://example.com", SecretKey: "test-secret"}}
	registrar := NewRegistrar(config.ClickStackConfig{}, service)
	catalog, err := tool.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if err := registrar.Ensure(context.Background(), catalog, tool.SurfacePolicy{Surface: "slack", AvailableDeps: map[string]bool{"slack": true, "clickstack": true, "clickstack-connection": true}}, "user-a"); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range catalog.Descriptors() {
		if strings.Contains(descriptor.Name, "i-a") || strings.Contains(descriptor.Description, "private") {
			t.Fatalf("catalog leaked user-specific connection: %+v", descriptor)
		}
	}

	call, _ := json.Marshal(map[string]any{"instance_id": "i-a", "tool_name": "logs"})
	result, err := (connectionCallTool{conn: service}).Execute(context.Background(), tool.Call{Arguments: call, Scope: tool.Scope{UserID: "user-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.ErrorCode != "connection_required" {
		t.Fatalf("user B used user A instance: %+v", result)
	}
}

func TestEmptyConnectionListReturnsConfigureAction(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	service := &connections.Service{Store: store}
	result, err := (connectionListTool{conn: service}).Execute(context.Background(), tool.Call{Scope: tool.Scope{UserID: "user-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.ErrorCode != "connection_required" {
		t.Fatalf("empty connection list result = %+v", result)
	}
	action, ok := result.Metadata["connection_action"].(connections.ConnectionAction)
	if !ok {
		t.Fatalf("connection action = %#v", result.Metadata["connection_action"])
	}
	if action.Provider != connections.ProviderClickStack || action.State != "configure" || action.URL != "" {
		t.Fatalf("connection action = %+v, want configure without URL", action)
	}
}

func TestConnectionCallRejectsUnannotatedRemoteToolBeforeExecution(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTokenInstance(context.Background(), "user-a", connections.ProviderClickStack, "i-a", "A server", "a-token", nil, "", map[string]string{"mcp_url": "https://a.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	service := &connections.Service{Store: store}
	var calls atomic.Int32
	clientFactory := func(_ connections.Connection, token string) *agentmcp.Client {
		return &agentmcp.Client{
			ServiceName: "test-clickstack",
			URL:         "https://a.example/mcp",
			Token:       token,
			HTTP: &http.Client{Transport: clickStackRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				var payload struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
				}
				body, readErr := io.ReadAll(request.Body)
				if readErr != nil {
					return nil, readErr
				}
				if err := json.Unmarshal(body, &payload); err != nil {
					return nil, err
				}
				if payload.Method == "tools/call" {
					calls.Add(1)
				}
				result := `{}`
				switch payload.Method {
				case "initialize":
					result = `{"protocolVersion":"2025-03-26","capabilities":{}}`
				case "notifications/initialized":
					return clickStackResponse(http.StatusAccepted, ""), nil
				case "tools/list":
					// No readOnlyHint is an intentional fail-closed case.
					result = `{"tools":[{"name":"mutating_tool","inputSchema":{"type":"object"}}]}`
				case "tools/call":
					result = `{"content":[{"type":"text","text":"should not execute"}]}`
				default:
					return nil, fmt.Errorf("unexpected MCP method %q", payload.Method)
				}
				response, marshalErr := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": json.RawMessage(result)})
				if marshalErr != nil {
					return nil, marshalErr
				}
				return clickStackResponse(http.StatusOK, string(response)), nil
			})},
		}
	}
	arguments, err := json.Marshal(map[string]any{
		"instance_id": "i-a",
		"tool_name":   "mutating_tool",
		"arguments":   map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (connectionCallTool{conn: service, clientFactory: clientFactory}).Execute(context.Background(), tool.Call{
		Arguments: arguments,
		Scope:     tool.Scope{UserID: "user-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.ErrorCode != "remote_tool_not_read_only" {
		t.Fatalf("result=%+v, want remote_tool_not_read_only", result)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("remote tools/call count=%d, want 0 for unannotated tool", got)
	}
}

func clickStackResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Mcp-Session-Id": []string{"remote"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
