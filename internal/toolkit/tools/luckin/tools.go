package luckin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

const defaultMCPURL = "https://gwmcp.lkcoffee.com/order/user/mcp"

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client

	nextID atomic.Int64
}

func (c *Client) enabled() bool {
	return strings.TrimSpace(c.Token) != ""
}

func (c *Client) endpoint() string {
	endpoint := strings.TrimSpace(c.URL)
	if endpoint == "" {
		return defaultMCPURL
	}
	return endpoint
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
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

func (t MCPTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	if t.Client == nil || !t.Client.enabled() {
		return registry.Result{}, fmt.Errorf("Luckin MCP is not configured: LUCKIN_MCP_TOKEN is required")
	}
	args := json.RawMessage(raw)
	if t.SideEffect {
		cleaned, err := requireConfirmation(raw)
		if err != nil {
			return registry.Result{}, err
		}
		args = cleaned
	}
	session, err := t.Client.session(ctx, rt.Cache)
	if err != nil {
		return registry.Result{}, err
	}
	out, err := t.Client.callTool(ctx, session, t.RemoteName, args)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}

func RegisterAll(reg *registry.Registry, client *Client) {
	for _, tool := range tools(client) {
		reg.Register(tool)
	}
}

func tools(client *Client) []MCPTool {
	return []MCPTool{
		{
			Client:      client,
			LocalName:   "luckin-query_shop_list",
			RemoteName:  "queryShopList",
			Description: "Query nearby Luckin Coffee stores. Use this before product search or ordering. Requires the user's approximate longitude and latitude; ask for location when missing.",
			Parameters: registry.ObjectSchema([]string{"longitude", "latitude"}, map[string]any{
				"deptName":  map[string]any{"type": "string", "description": "Optional store name filter."},
				"longitude": map[string]any{"type": "number", "description": "Longitude."},
				"latitude":  map[string]any{"type": "number", "description": "Latitude."},
			}),
		},
		{
			Client:      client,
			LocalName:   "luckin-search_product",
			RemoteName:  "searchProductForMcp",
			Description: "Search Luckin products in a store from the user's natural-language query. Use after choosing a store.",
			Parameters: registry.ObjectSchema([]string{"deptId", "query"}, map[string]any{
				"deptId": map[string]any{"type": "integer", "description": "Luckin store ID."},
				"query":  map[string]any{"type": "string", "description": "Original user product request, e.g. 冰美式 or 生椰拿铁少糖."},
			}),
		},
		{
			Client:      client,
			LocalName:   "luckin-switch_product",
			RemoteName:  "switchProduct",
			Description: "Switch a Luckin product option such as temperature, cup size, sugar, or ice. Use product attribute IDs returned by product search/detail.",
			Parameters: registry.ObjectSchema([]string{"deptId", "productId", "skuCode", "attrOperationParam", "amount"}, map[string]any{
				"deptId":    map[string]any{"type": "integer", "description": "Luckin store ID."},
				"productId": map[string]any{"type": "integer", "description": "Product ID."},
				"skuCode":   map[string]any{"type": "string", "description": "Current product SKU code."},
				"amount":    map[string]any{"type": "integer", "description": "Quantity."},
				"attrOperationParam": map[string]any{
					"type":        "object",
					"description": "Attribute switch operation.",
					"required":    []string{"attributeId", "subAttr"},
					"properties": map[string]any{
						"attributeId": map[string]any{"type": "integer", "description": "Attribute group ID."},
						"subAttr": map[string]any{
							"type":        "object",
							"description": "Target attribute value. operation must be 3 to select it.",
							"required":    []string{"attributeId", "operation"},
							"properties": map[string]any{
								"attributeId": map[string]any{"type": "integer", "description": "Attribute value ID."},
								"operation":   map[string]any{"type": "integer", "description": "Operation type. Use 3 to select."},
							},
						},
					},
				},
			}),
		},
		{
			Client:      client,
			LocalName:   "luckin-query_product_detail",
			RemoteName:  "queryProductDetailInfo",
			Description: "Query Luckin product details and selectable attributes for a store product.",
			Parameters: registry.ObjectSchema([]string{"deptId", "productId"}, map[string]any{
				"deptId":    map[string]any{"type": "integer", "description": "Luckin store ID."},
				"productId": map[string]any{"type": "integer", "description": "Product ID."},
			}),
		},
		{
			Client:      client,
			LocalName:   "luckin-preview_order",
			RemoteName:  "previewOrder",
			Description: "Preview a Luckin pickup order, including estimated price, applicable coupon codes, store info, and items. Use this before creating an order.",
			Parameters:  registry.ObjectSchema([]string{"deptId", "productList"}, orderProperties(false)),
		},
		{
			Client:      client,
			LocalName:   "luckin-query_coupons",
			RemoteName:  "previewOrder",
			Description: "Query applicable Luckin coupons and discounts for the selected store and products. This calls Luckin previewOrder; coupon codes are returned in couponCodeList and can be passed to luckin-create_order.",
			Parameters:  registry.ObjectSchema([]string{"deptId", "productList"}, orderProperties(false)),
		},
		{
			Client:      client,
			LocalName:   "luckin-create_order",
			RemoteName:  "createOrder",
			Description: "Create a Luckin order and return payment information. Only call after previewing the order and after the user explicitly confirms they want to place it. If a payOrderQrCodeUrl is returned, show it as the primary payment option because Slack or desktop clients may not complete WeChat Pay deep links directly.",
			Parameters:  registry.ObjectSchema([]string{"deptId", "productList", "longitude", "latitude", "confirmed"}, orderProperties(true)),
			SideEffect:  true,
		},
		{
			Client:      client,
			LocalName:   "luckin-query_order_detail",
			RemoteName:  "queryOrderDetailInfo",
			Description: "Query Luckin order status and pickup details.",
			Parameters: registry.ObjectSchema([]string{"orderId"}, map[string]any{
				"orderId": map[string]any{"type": "string", "description": "Luckin order ID."},
			}),
		},
		{
			Client:      client,
			LocalName:   "luckin-cancel_order",
			RemoteName:  "cancelOrder",
			Description: "Cancel a Luckin order. Only call when the user explicitly asks to cancel that order.",
			Parameters: registry.ObjectSchema([]string{"orderId", "confirmed"}, map[string]any{
				"orderId":   map[string]any{"type": "string", "description": "Luckin order ID."},
				"confirmed": map[string]any{"type": "boolean", "description": "Must be true only after the user explicitly asked to cancel this order."},
			}),
			SideEffect: true,
		},
	}
}

func orderProperties(includeLocation bool) map[string]any {
	props := map[string]any{
		"deptId": map[string]any{"type": "integer", "description": "Luckin store ID."},
		"productList": map[string]any{
			"type":        "array",
			"description": "Order item list.",
			"items": map[string]any{
				"type":     "object",
				"required": []string{"amount", "productId", "skuCode"},
				"properties": map[string]any{
					"amount":    map[string]any{"type": "integer", "description": "Quantity."},
					"productId": map[string]any{"type": "integer", "description": "Product ID."},
					"skuCode":   map[string]any{"type": "string", "description": "Product SKU code."},
				},
			},
		},
	}
	if includeLocation {
		props["longitude"] = map[string]any{"type": "number", "description": "Longitude."}
		props["latitude"] = map[string]any{"type": "number", "description": "Latitude."}
		props["couponCodeList"] = map[string]any{
			"type":        "array",
			"description": "Optional coupon code list from previewOrder.",
			"items":       map[string]any{"type": "string"},
		}
		props["confirmed"] = map[string]any{"type": "boolean", "description": "Must be true only after the user explicitly confirms order creation."}
	}
	return props
}

func requireConfirmation(raw json.RawMessage) (json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var confirmed bool
	if value, ok := payload["confirmed"]; ok {
		_ = json.Unmarshal(value, &confirmed)
	}
	if !confirmed {
		return nil, fmt.Errorf("explicit user confirmation is required before this Luckin action")
	}
	delete(payload, "confirmed")
	cleaned, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return cleaned, nil
}

type session struct {
	ID          string
	Initialized bool
}

const sessionCacheKey = "luckin-mcp-session"

func (c *Client) session(ctx context.Context, cache *registry.RuntimeCache) (session, error) {
	if cache != nil {
		if cached, ok := cache.Get(sessionCacheKey); ok {
			if s, ok := cached.(session); ok && s.Initialized {
				return s, nil
			}
		}
	}
	s, err := c.initialize(ctx)
	if err != nil {
		return session{}, err
	}
	if cache != nil {
		cache.Set(sessionCacheKey, s)
	}
	return s, nil
}

func (c *Client) initialize(ctx context.Context) (session, error) {
	result, headers, err := c.rpc(ctx, "", "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "wati-oncall-agent",
			"version": "0.1.0",
		},
	})
	if err != nil {
		return session{}, err
	}
	if len(result) == 0 {
		return session{}, fmt.Errorf("Luckin MCP initialize returned empty result")
	}
	s := session{ID: firstHeader(headers, "Mcp-Session-Id"), Initialized: true}
	_ = c.notifyInitialized(ctx, s.ID)
	return s, nil
}

func (c *Client) notifyInitialized(ctx context.Context, sessionID string) error {
	_, _, err := c.rpcWithID(ctx, sessionID, nil, "notifications/initialized", nil)
	return err
}

func (c *Client) callTool(ctx context.Context, s session, name string, args json.RawMessage) (string, error) {
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", err
		}
	}
	result, _, err := c.rpc(ctx, s.ID, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return "", err
	}
	return formatToolResult(name, result), nil
}

func (c *Client) rpc(ctx context.Context, sessionID string, method string, params any) (json.RawMessage, http.Header, error) {
	id := c.nextID.Add(1)
	return c.rpcWithID(ctx, sessionID, id, method, params)
}

func (c *Client) rpcWithID(ctx context.Context, sessionID string, id any, method string, params any) (json.RawMessage, http.Header, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		payload["id"] = id
	}
	if params != nil {
		payload["params"] = params
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.Header, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, fmt.Errorf("luckin mcp status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if id == nil {
		return nil, resp.Header, nil
	}
	result, err := parseRPCResponse(body, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, resp.Header, err
	}
	return result, resp.Header, nil
}

type rpcResponse struct {
	Result *json.RawMessage `json:"result"`
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

func parseRPCResponse(body []byte, contentType string) (json.RawMessage, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return parseSSERPCResponse(body)
	}
	return parseJSONRPCResponse(bytes.TrimSpace(body))
}

func parseSSERPCResponse(body []byte) (json.RawMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(dataLines) > 0 {
				if result, err := parseJSONRPCResponse([]byte(strings.Join(dataLines, "\n"))); err == nil {
					return result, nil
				}
				dataLines = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(dataLines) > 0 {
		return parseJSONRPCResponse([]byte(strings.Join(dataLines, "\n")))
	}
	return nil, fmt.Errorf("luckin mcp returned no JSON-RPC data")
}

func parseJSONRPCResponse(body []byte) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("luckin mcp returned empty response")
	}
	var resp rpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		if len(resp.Error.Data) > 0 {
			return nil, fmt.Errorf("luckin mcp error %d: %s: %s", resp.Error.Code, resp.Error.Message, string(resp.Error.Data))
		}
		return nil, fmt.Errorf("luckin mcp error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		return nil, fmt.Errorf("luckin mcp JSON-RPC message had no result")
	}
	return *resp.Result, nil
}

func formatToolResult(remoteName string, raw json.RawMessage) string {
	var parsed struct {
		Content []struct {
			Type string          `json:"type"`
			Text string          `json:"text,omitempty"`
			Data json.RawMessage `json:"data,omitempty"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return string(raw)
	}
	var parts []string
	if len(parsed.StructuredContent) > 0 {
		parts = append(parts, string(parsed.StructuredContent))
	}
	for _, item := range parsed.Content {
		switch {
		case item.Text != "":
			parts = append(parts, item.Text)
		case len(item.Data) > 0:
			parts = append(parts, string(item.Data))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, string(raw))
	}
	out := strings.Join(parts, "\n")
	if remoteName == "createOrder" {
		out = annotateCreateOrderPayment(out)
	}
	if parsed.IsError {
		return "[tool error] " + out
	}
	return out
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

func firstHeader(headers http.Header, key string) string {
	for _, candidate := range []string{key, strings.ToLower(key), strings.ToUpper(key)} {
		if value := strings.TrimSpace(headers.Get(candidate)); value != "" {
			return value
		}
	}
	return ""
}
