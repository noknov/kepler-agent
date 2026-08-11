package luckin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

// Client wraps a shared MCP client with Luckin-specific behavior.
type Client struct {
	MCP *mcp.Client
}

func (c *Client) enabled() bool {
	return c.MCP != nil && strings.TrimSpace(c.MCP.Token) != ""
}

type MCPTool struct {
	Client      *Client
	LocalName   string
	RemoteName  string
	Description string
	Parameters  map[string]any
	SideEffect  bool
}

func (t MCPTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(t.LocalName, t.Description, t.Parameters)
}

func (t MCPTool) IsWrite() bool { return t.SideEffect }

func (t MCPTool) Metadata() registry.ToolMetadata {
	risk := registry.RiskRead
	if t.SideEffect {
		risk = registry.RiskExternalWrite
	}
	return registry.ToolMetadata{
		Risk:         risk,
		Dependencies: []string{"luckin"},
		Surfaces:     []string{"slack"},
	}
}

func (t MCPTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Luckin MCP is not configured: LUCKIN_MCP_TOKEN is required")
	}
	// Side-effect capability is authorized by the server registry/policy. Do
	// not turn a model-generated boolean into a pretend user confirmation.
	args := json.RawMessage(raw)
	session, err := getOrCreateSession(ctx, t.Client.MCP, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.Client.MCP.CallTool(ctx, session, t.RemoteName, args)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out.Content}, nil
}

func RegisterAll(reg *registry.Registry, client *Client) {
	for _, tool := range tools(client) {
		reg.Register(tool)
	}
}

func RegisterDeferredAll(reg *registry.Registry, client *Client, category string) {
	for _, tool := range tools(client) {
		reg.RegisterDeferred(registry.AsDeferred(category, tool))
	}
}

func tools(client *Client) []MCPTool {
	return []MCPTool{
		{
			Client:     client,
			LocalName:  "luckin-query_shop_list",
			RemoteName: "queryShopList",
			Parameters: registry.ObjectSchema([]string{"longitude", "latitude"}, map[string]any{
				"deptName":  map[string]any{"type": "string", "description": ""},
				"longitude": map[string]any{"type": "number", "description": ""},
				"latitude":  map[string]any{"type": "number", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "luckin-search_product",
			RemoteName: "searchProductForMcp",
			Parameters: registry.ObjectSchema([]string{"deptId", "query"}, map[string]any{
				"deptId": map[string]any{"type": "integer", "description": ""},
				"query":  map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "luckin-switch_product",
			RemoteName: "switchProduct",
			Parameters: registry.ObjectSchema([]string{"deptId", "productId", "skuCode", "attrOperationParam", "amount"}, map[string]any{
				"deptId":    map[string]any{"type": "integer", "description": ""},
				"productId": map[string]any{"type": "integer", "description": ""},
				"skuCode":   map[string]any{"type": "string", "description": ""},
				"amount":    map[string]any{"type": "integer", "description": ""},
				"attrOperationParam": map[string]any{
					"type":        "object",
					"description": "",
					"required":    []string{"attributeId", "subAttr"},
					"properties": map[string]any{
						"attributeId": map[string]any{"type": "integer", "description": ""},
						"subAttr": map[string]any{
							"type":        "object",
							"description": "",
							"required":    []string{"attributeId", "operation"},
							"properties": map[string]any{
								"attributeId": map[string]any{"type": "integer", "description": ""},
								"operation":   map[string]any{"type": "integer", "description": ""},
							},
						},
					},
				},
			}),
		},
		{
			Client:     client,
			LocalName:  "luckin-query_product_detail",
			RemoteName: "queryProductDetailInfo",
			Parameters: registry.ObjectSchema([]string{"deptId", "productId"}, map[string]any{
				"deptId":    map[string]any{"type": "integer", "description": ""},
				"productId": map[string]any{"type": "integer", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "luckin-preview_order",
			RemoteName: "previewOrder",
			Parameters: registry.ObjectSchema([]string{"deptId", "productList"}, orderProperties(false)),
		},
		{
			Client:     client,
			LocalName:  "luckin-create_order",
			RemoteName: "createOrder",
			Parameters: registry.ObjectSchema([]string{"deptId", "productList", "longitude", "latitude"}, orderProperties(true)),
			SideEffect: true,
		},
		{
			Client:     client,
			LocalName:  "luckin-query_order_detail",
			RemoteName: "queryOrderDetailInfo",
			Parameters: registry.ObjectSchema([]string{"orderId"}, map[string]any{
				"orderId": map[string]any{"type": "string", "description": ""},
			}),
		},
		{
			Client:     client,
			LocalName:  "luckin-cancel_order",
			RemoteName: "cancelOrder",
			Parameters: registry.ObjectSchema([]string{"orderId"}, map[string]any{
				"orderId": map[string]any{"type": "string", "description": ""},
			}),
			SideEffect: true,
		},
	}
}

func orderProperties(includeLocation bool) map[string]any {
	props := map[string]any{
		"deptId": map[string]any{"type": "integer", "description": ""},
		"productList": map[string]any{
			"type":        "array",
			"description": "",
			"items": map[string]any{
				"type":     "object",
				"required": []string{"amount", "productId", "skuCode"},
				"properties": map[string]any{
					"amount":    map[string]any{"type": "integer", "description": ""},
					"productId": map[string]any{"type": "integer", "description": ""},
					"skuCode":   map[string]any{"type": "string", "description": ""},
				},
			},
		},
	}
	if includeLocation {
		props["longitude"] = map[string]any{"type": "number", "description": ""}
		props["latitude"] = map[string]any{"type": "number", "description": ""}
		props["couponCodeList"] = map[string]any{
			"type":        "array",
			"description": "",
			"items":       map[string]any{"type": "string"},
		}
	}
	return props
}

func getOrCreateSession(ctx context.Context, client *mcp.Client, cache *registry.RuntimeCache) (mcp.Session, error) {
	key := client.SessionKey()
	if cache != nil {
		if cached, ok := cache.Get(key); ok {
			if s, ok := cached.(mcp.Session); ok && s.Initialized {
				return s, nil
			}
		}
	}
	s, err := client.Initialize(ctx)
	if err != nil {
		return mcp.Session{}, err
	}
	if cache != nil {
		cache.Set(key, s)
	}
	return s, nil
}
