package luckin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestMCPToolCallsStreamableHTTP(t *testing.T) {
	var sessionID string
	var callCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q", got)
		}
		var payload struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		switch payload.Method {
		case "initialize":
			sessionID = "session-123"
			w.Header().Set("Mcp-Session-Id", sessionID)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`))
		case "notifications/initialized":
			if got := r.Header.Get("Mcp-Session-Id"); got != sessionID {
				t.Fatalf("initialized session = %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			callCount++
			if got := r.Header.Get("Mcp-Session-Id"); got != sessionID {
				t.Fatalf("tool session = %q", got)
			}
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(payload.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Name != "queryShopList" {
				t.Fatalf("tool name = %q", params.Name)
			}
			if params.Arguments["longitude"] != 118.08891 {
				t.Fatalf("arguments = %#v", params.Arguments)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\n"))
			_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}` + "\n\n"))
		default:
			t.Fatalf("unexpected method %q", payload.Method)
		}
	}))
	defer server.Close()

	client := &Client{URL: server.URL, Token: "token", HTTP: server.Client()}
	tool := MCPTool{
		Client:     client,
		LocalName:  "luckin-query_shop_list",
		RemoteName: "queryShopList",
		Parameters: registry.ObjectSchema([]string{"longitude", "latitude"}, map[string]any{
			"longitude": map[string]any{"type": "number"},
			"latitude":  map[string]any{"type": "number"},
		}),
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"longitude":118.08891,"latitude":24.479627}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q", result.Content)
	}
	if callCount != 1 {
		t.Fatalf("call count = %d", callCount)
	}
}

func TestSideEffectRequiresConfirmationAndStripsFlag(t *testing.T) {
	cleaned, err := requireConfirmation(json.RawMessage(`{"orderId":"123","confirmed":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleaned), "confirmed") {
		t.Fatalf("confirmed flag leaked to remote args: %s", cleaned)
	}
	if _, err := requireConfirmation(json.RawMessage(`{"orderId":"123"}`)); err == nil {
		t.Fatal("expected missing confirmation error")
	}
}

func TestRegisterAllIncludesCouponAlias(t *testing.T) {
	reg := registry.New()
	RegisterAll(reg, &Client{})
	names := strings.Join(reg.Names(), "\n")
	if !strings.Contains(names, "luckin-query_coupons") {
		t.Fatalf("registered names missing coupon alias:\n%s", names)
	}
}

func TestCreateOrderResultAnnotatesPaymentURLs(t *testing.T) {
	raw := json.RawMessage(`{
		"content": [{
			"type": "text",
			"text": "{\"code\":0,\"data\":{\"orderIdStr\":\"7620\",\"discountPrice\":9.9,\"needPay\":true,\"payOrderUrl\":\"weixin://pay\",\"payOrderQrCodeUrl\":\"https://example.test/qr.png\"}}"
		}]
	}`)
	got := formatToolResult("createOrder", raw)
	for _, want := range []string{
		"payQrCodeUrl=https://example.test/qr.png",
		"wechatPayUrl=weixin://pay",
		"Show payQrCodeUrl as the primary payment option",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted result missing %q:\n%s", want, got)
		}
	}
}
