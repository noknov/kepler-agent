package notion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
	agentmcp "github.com/noknov/kepler-agent/packages/mcp"
)

type notionRegistryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f notionRegistryRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNotionReadAndWriteRoutersEnforceRemoteEffects(t *testing.T) {
	store, err := connections.NewFileStore(t.TempDir()+"/connections.json", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTokenInstance(context.Background(), "user-a", connections.ProviderNotion, "i-a", "A workspace", "a-token", nil, "", nil); err != nil {
		t.Fatal(err)
	}
	service := &connections.Service{Store: store}
	var calls atomic.Int32
	clientFactory := func(_ connections.Connection, token string) *agentmcp.Client {
		return &agentmcp.Client{
			ServiceName: "test-notion",
			URL:         "https://notion.example/mcp",
			Token:       token,
			HTTP: &http.Client{Transport: notionRegistryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				var payload struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					return nil, err
				}
				result := `{}`
				switch payload.Method {
				case "initialize":
					result = `{"protocolVersion":"2025-03-26","capabilities":{}}`
				case "notifications/initialized":
					return notionRegistryResponse(http.StatusAccepted, ""), nil
				case "tools/list":
					// No readOnlyHint means this operation is not eligible for notion_call.
					result = `{"tools":[{"name":"update_page","inputSchema":{"type":"object"}}]}`
				case "tools/call":
					calls.Add(1)
					result = `{"content":[{"type":"text","text":"updated"}]}`
				}
				response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": json.RawMessage(result)})
				if err != nil {
					return nil, err
				}
				return notionRegistryResponse(http.StatusOK, string(response)), nil
			})},
		}
	}
	args, err := json.Marshal(map[string]any{
		"instance_id": "i-a",
		"tool_name":   "update_page",
		"arguments":   map[string]any{"page_id": "p1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readResult, err := (connectionCallTool{conn: service, clientFactory: clientFactory}).Execute(context.Background(), tool.Call{
		Arguments: args,
		Scope:     tool.Scope{UserID: "user-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !readResult.IsError || readResult.ErrorCode != "remote_tool_not_read_only" {
		t.Fatalf("read router result = %+v, want remote_tool_not_read_only", readResult)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("read router invoked tools/call %d times", got)
	}

	writeResult, err := (connectionWriteTool{conn: service, clientFactory: clientFactory}).Execute(context.Background(), tool.Call{
		Arguments: args,
		Scope:     tool.Scope{UserID: "user-a"},
	})
	if err != nil || writeResult.IsError || !strings.Contains(writeResult.Text(), "updated") {
		t.Fatalf("write router result = %+v, err=%v", writeResult, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("write router tools/call count=%d, want 1", got)
	}
}

func TestNotionWriteDescriptorIsExternalWriteAndNotParallel(t *testing.T) {
	descriptor := (connectionWriteTool{}).Descriptor()
	if err := tool.ValidateDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
	var externalWrite, network bool
	for _, effect := range descriptor.Effects {
		externalWrite = externalWrite || effect == tool.EffectExternalWrite
		network = network || effect == tool.EffectNetwork
	}
	if !externalWrite || !network {
		t.Fatalf("Notion write descriptor effects=%v, want external_write and network", descriptor.Effects)
	}
	if descriptor.Parallel {
		t.Fatal("Notion write descriptor must not be parallel")
	}
}

func notionRegistryResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Mcp-Session-Id": []string{"remote"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
