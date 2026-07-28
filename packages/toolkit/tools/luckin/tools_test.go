package luckin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/mcp"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestMCPToolCallsStreamableHTTP(t *testing.T) {
	var sessionID string
	var callCount int
	client := &Client{MCP: &mcp.Client{URL: "https://luckin.test/mcp", Token: "token", HTTP: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
				return httpResponse(http.StatusOK, map[string]string{
					"Mcp-Session-Id": sessionID,
					"Content-Type":   "application/json",
				}, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`), nil
			case "notifications/initialized":
				if got := r.Header.Get("Mcp-Session-Id"); got != sessionID {
					t.Fatalf("initialized session = %q", got)
				}
				return httpResponse(http.StatusAccepted, nil, ""), nil
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
				return httpResponse(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"},
					"event: message\n"+`data: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}`+"\n\n"), nil
			default:
				t.Fatalf("unexpected method %q", payload.Method)
			}
			return nil, nil
		}),
	}}}
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func httpResponse(status int, headers map[string]string, body string) *http.Response {
	h := http.Header{}
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestRegisterAllIncludesCouponAlias(t *testing.T) {
	reg := registry.New()
	RegisterAll(reg, &Client{MCP: &mcp.Client{Token: "x"}})
	names := strings.Join(reg.Names(), "\n")
	if !strings.Contains(names, "luckin-query_coupons") {
		t.Fatalf("registered names missing coupon alias:\n%s", names)
	}
}

func TestCreateOrderResultAnnotatesPaymentURLs(t *testing.T) {
	out := mcp.FormatToolResult(json.RawMessage(`{
		"content": [{
			"type": "text",
			"text": "{\"code\":0,\"data\":{\"orderIdStr\":\"7620\",\"discountPrice\":9.9,\"needPay\":true,\"payOrderUrl\":\"weixin://pay\",\"payOrderQrCodeUrl\":\"https://example.test/qr.png\"}}"
		}]
	}`))
	got := annotateCreateOrderPayment(out)
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
