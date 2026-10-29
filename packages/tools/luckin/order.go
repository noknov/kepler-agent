package luckin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

const previewedOrderCacheKey = "luckin:previewed_order"

type orderPreview struct {
	Fingerprint string `json:"fingerprint"`
	SessionID   string `json:"session_id"`
	TurnID      string `json:"turn_id"`
	UserID      string `json:"user_id"`
	Credential  string `json:"credential"`
}

func (c *Client) orderBindingVersion() string {
	var binding struct {
		URL, Token string
		Headers    map[string]string
	}
	if c != nil && c.MCP != nil {
		binding.URL, binding.Token, binding.Headers = c.MCP.URL, c.MCP.Token, c.MCP.Headers
	}
	raw, _ := json.Marshal(binding)
	return fmt.Sprintf("luckin-v2:%x", sha256.Sum256(raw))
}

func previewFor(client *Client, call tool.Call) orderPreview {
	return orderPreview{orderFingerprint(call.Arguments), call.Scope.SessionID, call.Scope.TurnID, call.Scope.UserID, client.orderBindingVersion()}
}

type previewOrderTool struct{ MCPTool }

func (t previewOrderTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	cache := tool.CacheFor(call.Scope)
	cache.Set(previewedOrderCacheKey, nil)
	result, err := t.MCPTool.Execute(ctx, call)
	if err == nil && !result.IsError {
		if proof := previewFor(t.Client, call); proof.Fingerprint != "" {
			cache.Set(previewedOrderCacheKey, proof)
		}
	}
	return result, err
}

type createOrderTool struct{ MCPTool }

func (t createOrderTool) PrepareCall(call tool.Call) (json.RawMessage, error) {
	proof := previewFor(t.Client, call)
	previewed, _ := tool.CacheFor(call.Scope).Get(previewedOrderCacheKey)
	if proof.Fingerprint == "" || previewed != proof {
		return nil, fmt.Errorf("preview the exact store and SKU list before requesting order creation")
	}
	return json.Marshal(proof)
}

func (t createOrderTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var proof orderPreview
	if json.Unmarshal(call.State, &proof) != nil || proof.Fingerprint == "" || proof != previewFor(t.Client, call) {
		result := tool.TextResult("Refusing to create order without a captured preview for this exact store, SKU list, actor, turn and credential. Call luckin-preview_order first and request a new order confirmation.")
		result.IsError, result.ErrorCode = true, "order_preview_required"
		return result, nil
	}
	return t.MCPTool.Execute(ctx, call)
}

func orderFingerprint(raw json.RawMessage) string {
	var payload struct {
		DeptID      int `json:"deptId"`
		ProductList []struct {
			ProductID int    `json:"productId"`
			SKUCode   string `json:"skuCode"`
			Amount    int    `json:"amount"`
		} `json:"productList"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.DeptID == 0 || len(payload.ProductList) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}
