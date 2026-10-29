package luckin

import (
	"context"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
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

func (t MCPTool) Descriptor() tool.Descriptor {
	effects := []tool.Effect{tool.EffectRead, tool.EffectNetwork}
	if t.SideEffect {
		effects = []tool.Effect{tool.EffectExternalWrite, tool.EffectNetwork}
	}
	d := tool.FunctionDescriptor(t.LocalName, t.Description, t.Parameters, tool.WithEffects(effects...), tool.WithDependencies("luckin"))
	if t.SideEffect {
		d.Version = t.Client.orderBindingVersion()
	}
	return d
}

func (t MCPTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return tool.Result{}, fmt.Errorf("Luckin MCP is not configured: LUCKIN_MCP_TOKEN is required")
	}
	cache := tool.CacheFor(call.Scope)
	session, err := getOrCreateSession(ctx, t.Client.MCP, cache)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := t.Client.MCP.CallTool(ctx, session, t.RemoteName, call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	return mcptools.Result(out), nil
}

// Tools returns the Luckin MCP tool inventory for catalog registration.
func Tools(client *Client) []tool.Tool {
	return []tool.Tool{
		MCPTool{
			Client:     client,
			LocalName:  "luckin-query_shop_list",
			RemoteName: "queryShopList",
			Parameters: tool.ObjectSchema([]string{"longitude", "latitude"}, map[string]any{
				"deptName":  map[string]any{"type": "string", "description": ""},
				"longitude": map[string]any{"type": "number", "description": ""},
				"latitude":  map[string]any{"type": "number", "description": ""},
			}),
		},
		MCPTool{
			Client:     client,
			LocalName:  "luckin-search_product",
			RemoteName: "searchProductForMcp",
			Parameters: tool.ObjectSchema([]string{"deptId", "query"}, map[string]any{
				"deptId": map[string]any{"type": "integer", "description": ""},
				"query":  map[string]any{"type": "string", "description": ""},
			}),
		},
		MCPTool{
			Client:     client,
			LocalName:  "luckin-switch_product",
			RemoteName: "switchProduct",
			Parameters: tool.ObjectSchema([]string{"deptId", "productId", "skuCode", "attrOperationParam", "amount"}, map[string]any{
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
		MCPTool{
			Client:     client,
			LocalName:  "luckin-query_product_detail",
			RemoteName: "queryProductDetailInfo",
			Parameters: tool.ObjectSchema([]string{"deptId", "productId"}, map[string]any{
				"deptId":    map[string]any{"type": "integer", "description": ""},
				"productId": map[string]any{"type": "integer", "description": ""},
			}),
		},
		previewOrderTool{MCPTool{
			Client:     client,
			LocalName:  "luckin-preview_order",
			RemoteName: "previewOrder",
			Parameters: tool.ObjectSchema([]string{"deptId", "productList"}, orderProperties(false)),
		}},
		createOrderTool{MCPTool{
			Client:     client,
			LocalName:  "luckin-create_order",
			RemoteName: "createOrder",
			Parameters: tool.ObjectSchema([]string{"deptId", "productList", "longitude", "latitude"}, orderProperties(true)),
			SideEffect: true,
		}},
		MCPTool{
			Client:     client,
			LocalName:  "luckin-query_order_detail",
			RemoteName: "queryOrderDetailInfo",
			Parameters: tool.ObjectSchema([]string{"orderId"}, map[string]any{
				"orderId": map[string]any{"type": "string", "description": ""},
			}),
		},
		MCPTool{
			Client:     client,
			LocalName:  "luckin-cancel_order",
			RemoteName: "cancelOrder",
			Parameters: tool.ObjectSchema([]string{"orderId"}, map[string]any{
				"orderId": map[string]any{"type": "string", "description": ""},
			}),
			SideEffect: true,
		},
	}
}

func orderProperties(includeLocation bool) map[string]any {
	props := map[string]any{
		"deptId": map[string]any{"type": "integer", "description": "Store ID (deptId)."},
		"productList": map[string]any{
			"type":        "array",
			"description": "Ordered items with productId, skuCode, and amount.",
			"items": map[string]any{
				"type":     "object",
				"required": []string{"amount", "productId", "skuCode"},
				"properties": map[string]any{
					"amount":    map[string]any{"type": "integer", "description": "Quantity of this SKU."},
					"productId": map[string]any{"type": "integer", "description": "Product ID."},
					"skuCode":   map[string]any{"type": "string", "description": "Resolved SKU code."},
				},
			},
		},
	}
	if includeLocation {
		props["longitude"] = map[string]any{"type": "number", "description": "Order longitude coordinate."}
		props["latitude"] = map[string]any{"type": "number", "description": "Order latitude coordinate."}
		props["couponCodeList"] = map[string]any{
			"type":        "array",
			"description": "Optional coupon codes to apply.",
			"items":       map[string]any{"type": "string"},
		}
	}
	return props
}

func getOrCreateSession(ctx context.Context, client *mcp.Client, cache *tool.TurnCache) (mcp.Session, error) {
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
