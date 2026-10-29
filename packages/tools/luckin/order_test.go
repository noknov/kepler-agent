package luckin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestCapturedOrderRejectsChangedBinding(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*tool.Call, *createOrderTool)
	}{
		{"store", func(c *tool.Call, _ *createOrderTool) {
			c.Arguments = json.RawMessage(`{"deptId":102,"productList":[{"productId":202,"skuCode":"sku","amount":1}]}`)
		}},
		{"session", func(c *tool.Call, _ *createOrderTool) { c.Scope.SessionID = "other" }},
		{"turn", func(c *tool.Call, _ *createOrderTool) { c.Scope.TurnID = "other" }},
		{"actor", func(c *tool.Call, _ *createOrderTool) { c.Scope.UserID = "other" }},
		{"token", func(_ *tool.Call, t *createOrderTool) { t.Client.MCP.Token = "other" }},
		{"endpoint", func(_ *tool.Call, t *createOrderTool) { t.Client.MCP.URL = "https://other.test/mcp" }},
		{"headers", func(_ *tool.Call, t *createOrderTool) { t.Client.MCP.Headers = map[string]string{"X-Account": "other"} }},
		{"missing state", func(c *tool.Call, _ *createOrderTool) { c.State = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			client := resultClient(t, `{"content":[{"type":"text","text":"ok"}]}`, &calls)
			call := tool.Call{Scope: tool.Scope{SessionID: t.Name(), TurnID: "turn", UserID: "user"}, Arguments: json.RawMessage(`{"deptId":101,"productList":[{"productId":202,"skuCode":"sku","amount":1}]}`)}
			t.Cleanup(func() { tool.ClearTurnCache(t.Name(), "turn") })
			preview := previewOrderTool{MCPTool{Client: client, LocalName: "custom-preview", RemoteName: "previewOrder"}}
			create := createOrderTool{MCPTool{Client: client, LocalName: "custom-create", RemoteName: "createOrder", SideEffect: true}}
			if result, err := preview.Execute(t.Context(), call); err != nil || result.IsError {
				t.Fatalf("preview failed: %+v %v", result, err)
			}
			prepared, err := tool.PrepareCall(create, call)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&prepared, &create)
			result, err := create.Execute(t.Context(), prepared)
			if err != nil || result.ErrorCode != "order_preview_required" || len(calls) != 1 {
				t.Fatalf("changed binding submitted order: calls=%v result=%+v err=%v", calls, result, err)
			}
		})
	}
}

func TestFailedPreviewInvalidatesPriorSuccess(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote error", true: "transport error"}[transportFailure], func(t *testing.T) {
			var calls []string
			client := resultClient(t, `{"content":[{"type":"text","text":"ok"}]}`, &calls)
			call := tool.Call{Scope: tool.Scope{SessionID: t.Name(), TurnID: "turn"}, Arguments: json.RawMessage(`{"deptId":101,"productList":[{"productId":202,"skuCode":"sku","amount":1}]}`)}
			t.Cleanup(func() { tool.ClearTurnCache(t.Name(), "turn") })
			preview := previewOrderTool{MCPTool{Client: client, RemoteName: "previewOrder"}}
			if result, err := preview.Execute(t.Context(), call); err != nil || result.IsError {
				t.Fatalf("first preview failed: %+v %v", result, err)
			}
			create := createOrderTool{MCPTool{Client: client, RemoteName: "createOrder", SideEffect: true}}
			preview.Client = resultClient(t, `{"isError":true,"content":[{"type":"text","text":"no longer available"}]}`, &calls)
			if transportFailure {
				preview.Client.MCP.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, http.ErrHandlerTimeout })}
			}
			if result, err := preview.Execute(t.Context(), call); err == nil && !result.IsError {
				t.Fatal("expected failed second preview")
			}
			if _, err := tool.PrepareCall(create, call); err == nil {
				t.Fatal("failed latest preview retained the prior success")
			}
		})
	}
}
