// Package mcp implements the MCP Streamable HTTP transport (JSON-RPC 2.0).
// It is used by tool integrations that proxy to remote MCP servers.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a reusable MCP Streamable HTTP client.
// Each integration creates its own Client instance with a distinct ServiceName.
type Client struct {
	ServiceName string // e.g. "luckin", "playwright" — used in session cache key
	URL         string // remote MCP endpoint
	Token       string // Bearer token; empty means no Authorization header
	// HTTP overrides the shared HTTP client. Set in tests to inject a mock transport.
	// Leave nil to use the lazily-initialized shared client (keep-alives enabled, 60s timeout).
	HTTP       *http.Client
	nextID     atomic.Int64
	sharedOnce sync.Once
	sharedHTTP *http.Client
}

// Session holds the MCP session state after initialization.
type Session struct {
	ID          string
	Initialized bool
}

// Endpoint returns the MCP server URL.
func (c *Client) Endpoint() string { return c.URL }

// SessionKey returns the RuntimeCache key for this client's session.
func (c *Client) SessionKey() string { return "mcp-session-" + c.ServiceName }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	c.sharedOnce.Do(func() {
		c.sharedHTTP = &http.Client{
			// 60s covers slow browser operations (navigation, screenshots, waits).
			Timeout: 60 * time.Second,
		}
	})
	return c.sharedHTTP
}

// Initialize performs the MCP initialize handshake and returns a session.
func (c *Client) Initialize(ctx context.Context) (Session, error) {
	result, headers, err := c.rpc(ctx, "", "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "slack-copilot-agent",
			"version": "0.1.0",
		},
	})
	if err != nil {
		return Session{}, err
	}
	if len(result) == 0 {
		return Session{}, fmt.Errorf("mcp %s: initialize returned empty result", c.ServiceName)
	}
	s := Session{ID: firstHeader(headers, "Mcp-Session-Id"), Initialized: true}
	if err := c.NotifyInitialized(ctx, s.ID); err != nil {
		return Session{}, fmt.Errorf("mcp %s: notifications/initialized: %w", c.ServiceName, err)
	}
	return s, nil
}

// NotifyInitialized sends the notifications/initialized message.
func (c *Client) NotifyInitialized(ctx context.Context, sessionID string) error {
	_, _, err := c.rpcWithID(ctx, sessionID, nil, "notifications/initialized", nil)
	return err
}

// CallTool invokes a remote MCP tool and returns the formatted result text.
func (c *Client) CallTool(ctx context.Context, s Session, name string, args json.RawMessage) (string, error) {
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
	return FormatToolResult(result), nil
}

func (c *Client) rpc(ctx context.Context, sessionID, method string, params any) (json.RawMessage, http.Header, error) {
	return c.rpcWithID(ctx, sessionID, c.nextID.Add(1), method, params)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint(), bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	if token := strings.TrimSpace(c.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
		return nil, resp.Header, fmt.Errorf("mcp %s: status %d: %s", c.ServiceName, resp.StatusCode, strings.TrimSpace(string(body)))
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
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(data))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(dataLines) > 0 {
		return parseJSONRPCResponse([]byte(strings.Join(dataLines, "\n")))
	}
	// Server sent only keepalive comments (e.g. ": keep-alive") or an empty stream.
	// Treat as a successful call that returned no content rather than a hard error.
	empty := json.RawMessage(`{"content":[]}`)
	return empty, nil
}

func parseJSONRPCResponse(body []byte) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("mcp: empty JSON-RPC response")
	}
	var resp rpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		if len(resp.Error.Data) > 0 {
			return nil, fmt.Errorf("mcp error %d: %s: %s", resp.Error.Code, resp.Error.Message, string(resp.Error.Data))
		}
		return nil, fmt.Errorf("mcp error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		return nil, fmt.Errorf("mcp JSON-RPC response had no result")
	}
	return *resp.Result, nil
}

// FormatToolResult parses an MCP tools/call result and returns the concatenated content.
// Image items (type "image") are returned as data URIs so multimodal models can consume them.
func FormatToolResult(raw json.RawMessage) string {
	var parsed struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			Data     string `json:"data,omitempty"` // base64-encoded; present on image items
			MimeType string `json:"mimeType,omitempty"`
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
		switch item.Type {
		case "text":
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		case "image":
			if item.Data != "" {
				mimeType := item.MimeType
				if mimeType == "" {
					mimeType = "image/png"
				}
				// Validate the base64 payload before constructing the data URI.
				if _, err := base64.StdEncoding.DecodeString(item.Data); err == nil {
					parts = append(parts, "data:"+mimeType+";base64,"+item.Data)
				} else {
					parts = append(parts, "[image: invalid base64]")
				}
			}
		default:
			// Unknown content types: include raw text if present, otherwise skip.
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, string(raw))
	}
	out := strings.Join(parts, "\n")
	if parsed.IsError {
		return "[tool error] " + out
	}
	return out
}

func firstHeader(headers http.Header, key string) string {
	for _, candidate := range []string{key, strings.ToLower(key), strings.ToUpper(key)} {
		if value := strings.TrimSpace(headers.Get(candidate)); value != "" {
			return value
		}
	}
	return ""
}
