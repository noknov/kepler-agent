package luckin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
)

func TestMCPToolPreservesRemoteOutcome(t *testing.T) {
	for _, test := range []struct {
		name, result string
		error        bool
		image        bool
	}{
		{"failed preview", `{"isError":true,"content":[{"type":"text","text":"invalid product"}]}`, true, false},
		{"image result", `{"content":[{"type":"image","mimeType":"image/png","data":"aGVsbG8="}]}`, false, true},
		{"successful preview", `{"content":[{"type":"text","text":"preview accepted"}]}`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var remoteCalls []string
			client := resultClient(t, test.result, &remoteCalls)
			scope := tool.Scope{SessionID: t.Name(), TurnID: "turn"}
			t.Cleanup(func() { tool.ClearTurnCache(scope.SessionID, scope.TurnID) })
			args := json.RawMessage(`{"deptId":101,"productList":[{"productId":202,"skuCode":"sku","amount":1}]}`)
			preview := previewOrderTool{MCPTool{Client: client, LocalName: "luckin-preview_order", RemoteName: "previewOrder"}}
			result, err := preview.Execute(t.Context(), tool.Call{Scope: scope, Arguments: args})
			if err != nil || result.IsError != test.error || (result.ErrorCode == "mcp_tool_error") != test.error {
				t.Errorf("remote outcome lost: result=%+v err=%v", result, err)
			}
			if test.image && (len(result.Content) != 1 || result.Content[0].Type != model.ContentImage || result.Content[0].ImageURL != "data:image/png;base64,aGVsbG8=") {
				t.Errorf("remote image lost: %+v", result)
			}
			create := createOrderTool{MCPTool{Client: client, LocalName: "luckin-create_order", RemoteName: "createOrder", SideEffect: true}}
			call, prepareErr := tool.PrepareCall(create, tool.Call{Scope: scope, Arguments: args})
			if test.error {
				if prepareErr == nil || len(remoteCalls) != 1 {
					t.Fatalf("failed preview permitted order preparation: calls=%v err=%v", remoteCalls, prepareErr)
				}
			} else {
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				result, err = create.Execute(t.Context(), call)
				if err != nil || result.IsError || len(remoteCalls) != 2 || remoteCalls[1] != "createOrder" {
					t.Fatalf("successful preview lost: calls=%v result=%+v err=%v", remoteCalls, result, err)
				}
			}
		})
	}
}

func resultClient(t *testing.T, remoteResult string, remoteCalls *[]string) *Client {
	t.Helper()
	return &Client{MCP: &mcp.Client{URL: "https://luckin.test/mcp", Token: "token", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		result := remoteResult
		switch request.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-11-25","capabilities":{}}`
		case "notifications/initialized":
			return httpResponse(http.StatusAccepted, nil, ""), nil
		case "tools/call":
			*remoteCalls = append(*remoteCalls, request.Params.Name)
		}
		return httpResponse(http.StatusOK, map[string]string{"Content-Type": "application/json"}, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, request.ID, result)), nil
	})}}}
}

func TestCreateOrderRequiresExactOpaqueSKU(t *testing.T) {
	for _, test := range []struct{ name, preview, create string }{
		{"separators", `{"deptId":101,"productList":[{"productId":202,"skuCode":"sku","amount":1},{"productId":203,"skuCode":"other","amount":1}]}`, `{"deptId":101,"productList":[{"productId":202,"skuCode":"sku:1|203:other","amount":1}]}`},
		{"whitespace", `{"deptId":101,"productList":[{"productId":202,"skuCode":"sku","amount":1}]}`, `{"deptId":101,"productList":[{"productId":202,"skuCode":"sku ","amount":1}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var remoteCalls []string
			client := resultClient(t, `{"content":[{"type":"text","text":"ok"}]}`, &remoteCalls)
			scope := tool.Scope{SessionID: t.Name(), TurnID: "turn"}
			t.Cleanup(func() { tool.ClearTurnCache(scope.SessionID, scope.TurnID) })
			preview := previewOrderTool{MCPTool{Client: client, LocalName: "luckin-preview_order", RemoteName: "previewOrder"}}
			if result, err := preview.Execute(t.Context(), tool.Call{Scope: scope, Arguments: json.RawMessage(test.preview)}); err != nil || result.IsError {
				t.Fatalf("preview failed: %+v %v", result, err)
			}
			create := createOrderTool{MCPTool{Client: client, LocalName: "luckin-create_order", RemoteName: "createOrder", SideEffect: true}}
			_, err := tool.PrepareCall(create, tool.Call{Scope: scope, Arguments: json.RawMessage(test.create)})
			if err == nil || len(remoteCalls) != 1 {
				t.Fatalf("changed SKU passed preview binding: calls=%v err=%v", remoteCalls, err)
			}
		})
	}
}
