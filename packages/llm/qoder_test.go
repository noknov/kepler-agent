package llm

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type qoderTestTransport func(*http.Request) (*http.Response, error)

func (f qoderTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func qoderTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func qoderFixture(chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		b, _ := json.Marshal(map[string]any{"statusCodeValue": 200, "body": chunk})
		fmt.Fprintf(&out, "data: %s\n\n", b)
	}
	return out.String()
}
func TestQoderNativeRequestAndCache(t *testing.T) {
	c, err := NewQoderClient(QoderBaseURL, "synthetic-pat", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	counts := map[string]int{}
	c.httpClient.Transport = qoderTestTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		counts[r.URL.Path]++
		switch r.URL.Path {
		case "/api/v1/jobToken/exchange":
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"personal_token":"synthetic-pat"}` {
				t.Errorf("bad exchange body")
			}
			return qoderTestResponse(fmt.Sprintf(`{"token":"synthetic-job","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))), nil
		case "/api/v1/userinfo":
			if r.Header.Get("Authorization") != "Bearer synthetic-job" {
				t.Errorf("bad identity authorization")
			}
			return qoderTestResponse(`{"id":"synthetic-user","organization_id":"synthetic-org"}`), nil
		case "/algo/api/v2/model/list":
			checkQoderSignature(t, r, nil)
			return qoderTestResponse(`{"assistant":[{"key":"model-live","display_name":"Live Model","enable":true,"source":"system"},{"key":"disabled","enable":false}]}`), nil
		case qoderChatPath:
			body, _ := io.ReadAll(r.Body)
			checkQoderSignature(t, r, body)
			third := len(body) / 3
			normalized := append(append(append([]byte{}, body[len(body)-third:]...), body[third:len(body)-third]...), body[:third]...)
			raw, err := qoderEncoding.DecodeString(string(normalized))
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Session    string         `json:"session_id"`
				RequestID  string         `json:"request_id"`
				Messages   []Message      `json:"messages"`
				Tools      []ToolSpec     `json:"tools"`
				Config     QoderModel     `json:"model_config"`
				Parameters map[string]any `json:"parameters"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Config.Key != "model-live" || payload.Session != "session-1" || payload.RequestID != "request-1" || r.Header.Get("X-Model-Key") != "model-live" {
				t.Errorf("wrong model or request identity: %+v", payload)
			}
			if len(payload.Messages) != 3 || payload.Messages[1].Content != " " || payload.Messages[2].ToolCallID != "call-1" {
				t.Errorf("tool replay lost: %+v", payload.Messages)
			}
			if payload.Parameters["max_tokens"] != float64(128) || payload.Parameters["enable_thinking"] != false {
				t.Errorf("wrong parameters: %v", payload.Parameters)
			}
			return qoderTestResponse(qoderFixture(`{"choices":[{"delta":{"reasoning_content":"private reasoning","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4},"credits":{"total":0.01}}}`, "[DONE]")), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})
	req := Request{Model: "Live Model", Metadata: map[string]string{"session_id": "session-1", "request_id": "request-1"}, MaxTokens: 128, Thinking: "disabled", Messages: []Message{{Role: "user", Content: "lookup"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Function: ToolFunction{Name: "lookup", Arguments: `{}`}}}}, {Role: "tool", ToolCallID: "call-1", Content: "value"}}}
	var visible string
	for range 2 {
		r, err := c.ChatStream(context.Background(), req, TextStream(func(d TextDelta) { visible += d.Text }))
		if err != nil {
			t.Fatal(err)
		}
		if r.Message.Content != "OK" || r.Message.ReasoningContent != "private reasoning" || !r.Usage.Reported || r.Usage.CacheReadInputTokens != 4 || !strings.Contains(string(r.Raw), `"credits"`) {
			t.Fatalf("unexpected result: %+v", r)
		}
	}
	if visible != "OKOK" {
		t.Fatalf("reasoning leaked: %q", visible)
	}
	for _, p := range []string{"/api/v1/jobToken/exchange", "/api/v1/userinfo", "/algo/api/v2/model/list"} {
		if counts[p] != 1 {
			t.Fatalf("no cache for %s: %d", p, counts[p])
		}
	}
	if req.Messages[1].Content != "" {
		t.Fatal("request mutated")
	}
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	models[0].Config[0] = '!'
	models[0].Key = "mutated"
	models, err = c.Models(context.Background())
	if err != nil || models[0].Key != "model-live" || models[0].Config[0] != '{' {
		t.Fatal("catalog cache leaked mutable state")
	}
}
func checkQoderSignature(t *testing.T, r *http.Request, body []byte) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer COSY."), ".")
	if len(parts) != 2 {
		t.Fatal("missing COSY signature")
	}
	digest := md5.Sum([]byte(strings.Join([]string{parts[0], r.Header.Get("Cosy-Key"), r.Header.Get("Cosy-Date"), string(body), strings.TrimPrefix(r.URL.Path, "/algo")}, "\n")))
	if parts[1] != hex.EncodeToString(digest[:]) {
		t.Fatal("invalid signed body/path")
	}
	payload, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "synthetic-job") || strings.Contains(string(body), "synthetic-pat") {
		t.Fatal("plaintext credentials leaked")
	}
	if r.Header.Get("Cosy-Organization-Id") != "synthetic-org" {
		t.Fatal("lost organization")
	}
}
func TestQoderStreamToolsAndFailures(t *testing.T) {
	first := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"key\":"}}]}}]}`
	second := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"\"alpha\"}"}}]},"finish_reason":"tool_calls"}]}`
	completed := 0
	r, err := readQoderStream(strings.NewReader(qoderFixture(first, second, "[DONE]")), StreamHandler{OnToolCallComplete: func(call ToolCall) { completed++ }})
	if err != nil || completed != 1 || len(r.Message.ToolCalls) != 1 || r.Message.ToolCalls[0].Function.Arguments != `{"key":"alpha"}` {
		t.Fatalf("tool stream: %+v %v", r, err)
	}
	for name, fixture := range map[string]string{
		"truncated":              qoderFixture(`{"choices":[{"delta":{"content":"partial"}}]}`),
		"missing terminal":       qoderFixture(`{"choices":[{"delta":{"content":"partial"}}]}`, "[DONE]"),
		"missing done":           qoderFixture(`{"choices":[{"delta":{"content":"partial"},"finish_reason":"stop"}]}`),
		"empty":                  qoderFixture(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]"),
		"malformed":              "data: garbage\n\n",
		"native error":           qoderFixture(`{"code":"105","message":"synthetic-token-must-not-leak"}`),
		"http200 envelope error": `data: {"statusCodeValue":403,"body":"synthetic-token-must-not-leak"}` + "\n\n",
		"event error":            "event: error\ndata: {\"message\":\"synthetic-token-must-not-leak\"}\n\n",
		"missing tool":           qoderFixture(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]"),
		"negative tool index":    qoderFixture(`{"choices":[{"delta":{"tool_calls":[{"index":-1}]},"finish_reason":"tool_calls"}]}`, "[DONE]"),
	} {
		t.Run(name, func(t *testing.T) {
			completed := 0
			_, err := readQoderStream(strings.NewReader(fixture), StreamHandler{OnToolCallComplete: func(ToolCall) { completed++ }})
			if err == nil || completed != 0 {
				t.Fatalf("failed stream accepted: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-token-must-not-leak") {
				t.Fatal("secret error body leaked")
			}
		})
	}
}
func TestQoderUsageOnlyCreditsNotTokens(t *testing.T) {
	r, err := readQoderStream(strings.NewReader(qoderFixture(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"credits":{"total":0.2}}}`, "[DONE]")), StreamHandler{})
	if err != nil || r.Usage.Reported || !strings.Contains(string(r.Raw), `"credits"`) {
		t.Fatalf("credits confused with tokens: %+v %v", r, err)
	}
}
func TestQoderExpiredCredentialsAndConcurrentCatalog(t *testing.T) {
	c, _ := NewQoderClient(QoderBaseURL, "synthetic-pat", time.Second)
	c.auth = qoderAuth{token: "expired", expires: time.Now().Add(-time.Hour)}
	var exchanges int
	c.httpClient.Transport = qoderTestTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v1/jobToken/exchange":
			exchanges++
			return qoderTestResponse(fmt.Sprintf(`{"token":"fresh","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))), nil
		case "/api/v1/userinfo":
			return qoderTestResponse(`{"id":"user"}`), nil
		default:
			return qoderTestResponse(`{"assistant":[{"key":"live","enable":true}]}`), nil
		}
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Models(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if exchanges != 1 {
		t.Fatalf("concurrent token exchange: %d", exchanges)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Models(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
func TestQoderRejectsUnknownModelsAndEndpoints(t *testing.T) {
	if _, err := NewQoderClient("https://untrusted.example", "pat", time.Second); err == nil {
		t.Fatal("accepted untrusted endpoint")
	}
	c, _ := NewQoderClient(QoderBaseURL, "pat", time.Second)
	c.auth = qoderAuth{token: "job", expires: time.Now().Add(time.Hour)}
	c.models = []QoderModel{{Key: "live", Enabled: true}}
	c.catalogExpires = time.Now().Add(time.Hour)
	if _, err := c.Chat(context.Background(), Request{Model: "unknown"}); err == nil {
		t.Fatal("unknown model silently mapped")
	}
	body, err := c.requestBody(Request{ToolChoice: "none", Tools: []ToolSpec{{Type: "function"}}}, QoderModel{Config: json.RawMessage(`{"key":"live"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	_ = json.Unmarshal(body, &data)
	if string(data["tools"]) != "[]" {
		t.Fatalf("none still exposes tools: %s", data["tools"])
	}
}

func TestQoderAuthFailureDoesNotExposeCredentials(t *testing.T) {
	c, _ := NewQoderClient(QoderBaseURL, "synthetic-pat", time.Second)
	c.httpClient.Transport = qoderTestTransport(func(*http.Request) (*http.Response, error) {
		r := qoderTestResponse(`{"message":"echo synthetic-pat and synthetic-job"}`)
		r.StatusCode = 401
		return r, nil
	})
	_, err := c.Models(context.Background())
	var providerErr ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != 401 || strings.Contains(err.Error(), "synthetic-pat") || strings.Contains(err.Error(), "synthetic-job") {
		t.Fatalf("unsafe authentication error: %v", err)
	}
}
