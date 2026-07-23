package luckin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/mcp"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
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
	if t.RemoteName == "createOrder" {
		out = annotateCreateOrderPayment(out)
	}
	return registry.Result{Content: out}, nil
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
			LocalName:  "luckin-query_coupons",
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

func annotateCreateOrderPayment(out string) string {
	data := extractLuckinData(out)
	if len(data) == 0 {
		return out
	}
	payURL, _ := data["payOrderUrl"].(string)
	qrURL, _ := data["payOrderQrCodeUrl"].(string)
	if payURL == "" && qrURL == "" {
		return out
	}
	var lines []string
	lines = append(lines, "Luckin order payment information:")
	if orderID := stringifyJSONValue(data["orderIdStr"]); orderID != "" {
		lines = append(lines, "orderId="+orderID)
	} else if orderID := stringifyJSONValue(data["orderId"]); orderID != "" {
		lines = append(lines, "orderId="+orderID)
	}
	if price := stringifyJSONValue(data["discountPrice"]); price != "" {
		lines = append(lines, "discountPrice="+price)
	}
	if needPay := stringifyJSONValue(data["needPay"]); needPay != "" {
		lines = append(lines, "needPay="+needPay)
	}
	if qrURL != "" {
		lines = append(lines, "payQrCodeUrl="+qrURL)
	}
	if payURL != "" {
		lines = append(lines, "wechatPayUrl="+payURL)
	}
	lines = append(lines, "Payment note: Slack or desktop clients may only jump to WeChat for the WeChat Pay URL and may not finish payment directly. Show payQrCodeUrl as the primary payment option so the user can scan/open it in WeChat.")
	lines = append(lines, "Raw response:")
	lines = append(lines, out)
	return strings.Join(lines, "\n")
}

func extractLuckinData(out string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		return nil
	}
	if data, ok := parsed["data"].(map[string]any); ok {
		return data
	}
	return parsed
}

func stringifyJSONValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}
