// Package mcp implements the MCP Streamable HTTP transport (JSON-RPC 2.0).
// It is used by tool integrations that proxy to remote MCP servers.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes  = 8 << 20
	maxDiscoveryPages = 128
)

// Client is a reusable MCP Streamable HTTP client.
// Each integration creates its own Client instance with a distinct ServiceName.
type Client struct {
	ServiceName string // Stable integration name used in the session cache key.
	URL         string // remote MCP endpoint
	Token       string // Bearer token; empty means no Authorization header
	Headers     map[string]string
	// HTTP overrides the shared HTTP client. Set in tests to inject a mock transport.
	// Leave nil to use the lazily-initialized shared client (keep-alives enabled, 60s timeout).
	HTTP       *http.Client
	sharedOnce sync.Once
	sharedHTTP *http.Client
}

// Session holds the MCP session state after initialization.
// Session carries the negotiated version even for stateless servers with no ID.
type Session struct {
	ProtocolVersion string
	ID              string
	Initialized     bool
}

type ToolDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	InputSchema json.RawMessage  `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations is the MCP tool behavior hint. Hints are untrusted metadata:
// callers may use an explicit readOnlyHint=true to narrow a tool to read-only,
// but absence or false must fail closed as a potentially mutating operation.
type ToolAnnotations struct {
	ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`
}

type HTTPError struct {
	Service    string
	StatusCode int
	Body       string
}

func (e HTTPError) Error() string {
	return fmt.Sprintf("mcp %s: status %d: %s", e.Service, e.StatusCode, e.Body)
}

type RPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e RPCError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("mcp error %d: %s: %s", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message)
}

type ToolResult struct {
	Content           string
	StructuredContent json.RawMessage
	Images            []Image
	IsError           bool
}

type Image struct {
	MIMEType string
	Data     string
}

func (i Image) DataURI() string { return "data:" + i.MIMEType + ";base64," + i.Data }

func (c *Client) ListTools(ctx context.Context, session Session) ([]ToolDefinition, error) {
	result, _, err := c.rpc(ctx, session, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var page struct {
		Tools      []ToolDefinition `json:"tools"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(result, &page); err != nil {
		return nil, err
	}
	tools := append([]ToolDefinition(nil), page.Tools...)
	bytesRead := len(result)
	pages := 1
	seen := make(map[string]bool)
	names := make(map[string]bool)
	for _, item := range tools {
		if item.Name == "" || names[item.Name] {
			return nil, fmt.Errorf("mcp: invalid or duplicate tool name")
		}
		names[item.Name] = true
	}
	for page.NextCursor != "" {
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("mcp: discovery cursor repeated")
		}
		if pages >= maxDiscoveryPages {
			return nil, fmt.Errorf("mcp: discovery exceeds page limit")
		}
		pages++
		seen[page.NextCursor] = true
		result, _, err = c.rpc(ctx, session, "tools/list", map[string]any{"cursor": page.NextCursor})
		if err != nil {
			return nil, err
		}
		bytesRead += len(result)
		if bytesRead > maxResponseBytes {
			return nil, fmt.Errorf("mcp: discovery exceeds byte limit")
		}
		page = struct {
			Tools      []ToolDefinition `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}{}
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Tools {
			if item.Name == "" || names[item.Name] {
				return nil, fmt.Errorf("mcp: invalid or duplicate tool name")
			}
			names[item.Name] = true
		}
		tools = append(tools, page.Tools...)
	}
	return tools, nil
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
	result, headers, err := c.rpc(ctx, Session{}, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "kepler-agent",
			"version": "0.1.0",
		},
	})
	if err != nil {
		return Session{}, err
	}
	if len(result) == 0 {
		return Session{}, fmt.Errorf("mcp %s: initialize returned empty result", c.ServiceName)
	}
	var negotiated struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &negotiated) != nil {
		return Session{}, fmt.Errorf("mcp: invalid initialization result")
	}
	switch negotiated.ProtocolVersion {
	case "2025-03-26", "2025-06-18", "2025-11-25":
	default:
		return Session{}, fmt.Errorf("mcp: unsupported negotiated protocol version")
	}
	s := Session{ID: firstHeader(headers, "Mcp-Session-Id"), Initialized: true, ProtocolVersion: negotiated.ProtocolVersion}
	if err := c.NotifyInitialized(ctx, s); err != nil {
		return Session{}, fmt.Errorf("mcp %s: notifications/initialized: %w", c.ServiceName, err)
	}
	return s, nil
}

// NotifyInitialized completes initialization with the negotiated session.
func (c *Client) NotifyInitialized(ctx context.Context, session Session) error {
	_, _, err := c.rpcWithID(ctx, session, nil, "notifications/initialized", nil)
	return err
}

// CallTool invokes a remote MCP tool and returns the formatted result text.
func (c *Client) CallTool(ctx context.Context, s Session, name string, args json.RawMessage) (ToolResult, error) {
	arguments := json.RawMessage(`{}`)
	if len(args) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(args, &object); err != nil {
			return ToolResult{}, err
		}
		if object == nil {
			return ToolResult{}, fmt.Errorf("MCP tool arguments must be an object")
		}
		arguments = args
	}
	result, _, err := c.rpc(ctx, s, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return ToolResult{}, err
	}
	return FormatToolResult(result)
}

func (c *Client) rpc(ctx context.Context, session Session, method string, params any) (json.RawMessage, http.Header, error) {
	id := rand.Text()
	return c.rpcWithID(ctx, session, &id, method, params)
}

func (c *Client) rpcWithID(ctx context.Context, session Session, id *string, method string, params any) (json.RawMessage, http.Header, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		payload["id"] = *id
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
	for key, value := range c.Headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	if token := strings.TrimSpace(c.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if session.ID != "" {
		req.Header.Set("Mcp-Session-Id", session.ID)
	}
	if session.ProtocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", session.ProtocolVersion)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if readErr != nil {
			return nil, resp.Header, readErr
		}
		return nil, resp.Header, HTTPError{Service: c.ServiceName, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if id == nil {
		return nil, resp.Header, nil
	}
	result, err := readRPCResponse(resp.Body, resp.Header.Get("Content-Type"), *id)
	return result, resp.Header, err
}

// An SSE response may remain open after our result. Stop at the matching
// response, propagate its RPC error, and close the body instead of awaiting EOF.
func readRPCResponse(reader io.Reader, contentType string, expected string) (json.RawMessage, error) {
	mediaType := ""
	if contentType != "" {
		var err error
		mediaType, _, err = mime.ParseMediaType(contentType)
		if err != nil {
			return nil, fmt.Errorf("mcp: invalid response Content-Type: %w", err)
		}
	}
	bounded := io.LimitReader(reader, maxResponseBytes+1)
	if mediaType != "text/event-stream" {
		body, err := io.ReadAll(bounded)
		if err != nil {
			return nil, err
		}
		if len(body) > maxResponseBytes {
			return nil, fmt.Errorf("mcp: response exceeds byte limit")
		}
		result, matched, err := decodeRPCResponse(body, expected)
		if !matched && err == nil {
			err = fmt.Errorf("mcp: response ID does not match request")
		}
		return result, err
	}
	scanner := bufio.NewScanner(bounded)
	scanner.Buffer(make([]byte, 0, 64<<10), maxResponseBytes)
	var data []string
	flush := func() (json.RawMessage, bool, error) {
		if len(data) == 0 {
			return nil, false, nil
		}
		raw := []byte(strings.Join(data, "\n"))
		data = nil
		// Empty data events can prime resumable SSE without carrying an RPC.
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, false, nil
		}
		return decodeRPCResponse(raw, expected)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			result, matched, err := flush()
			if err != nil || matched {
				return result, err
			}
		} else if text, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(text, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if result, matched, err := flush(); err != nil || matched {
		return result, err
	}
	return nil, fmt.Errorf("mcp: stream ended or exceeded byte limit without a matching response")
}

func decodeRPCResponse(body json.RawMessage, expected string) (json.RawMessage, bool, error) {
	var response struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data,omitempty"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.Version != "2.0" {
		return nil, false, fmt.Errorf("mcp: invalid JSON-RPC response")
	}
	var responseID string
	if json.Unmarshal(response.ID, &responseID) != nil || responseID != expected {
		return nil, false, nil
	}
	if (response.Result != nil) == (response.Error != nil) {
		return nil, true, fmt.Errorf("mcp: response must contain exactly one result or error")
	}
	if response.Error != nil {
		return nil, true, RPCError{Code: response.Error.Code, Message: response.Error.Message, Data: response.Error.Data}
	}
	result := bytes.TrimSpace(response.Result)
	if len(result) == 0 || result[0] != '{' {
		return nil, true, fmt.Errorf("mcp: result must be an object")
	}
	return result, true, nil
}

// FormatToolResult preserves structured JSON separately from text and images.
func FormatToolResult(raw json.RawMessage) (ToolResult, error) {
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
		return ToolResult{}, err
	}
	var parts []string
	var images []Image
	if len(parsed.StructuredContent) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(parsed.StructuredContent, &object); err != nil || object == nil {
			return ToolResult{}, fmt.Errorf("mcp structuredContent must be a JSON object")
		}
	}
	for _, item := range parsed.Content {
		switch item.Type {
		case "text":
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		case "image":
			if item.Data != "" {
				if item.MimeType == "" {
					return ToolResult{}, fmt.Errorf("mcp image content is missing mimeType")
				}
				if _, err := base64.StdEncoding.DecodeString(item.Data); err != nil {
					return ToolResult{}, fmt.Errorf("mcp image content has invalid base64: %w", err)
				}
				images = append(images, Image{MIMEType: item.MimeType, Data: item.Data})
			}
		default:
			return ToolResult{}, fmt.Errorf("unsupported MCP content type %q", item.Type)
		}
	}
	return ToolResult{Content: strings.Join(parts, "\n"), StructuredContent: parsed.StructuredContent, Images: images, IsError: parsed.IsError}, nil
}

func firstHeader(headers http.Header, key string) string {
	return strings.TrimSpace(headers.Get(key))
}
