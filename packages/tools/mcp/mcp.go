// Package mcptools adapts configured MCP servers into agent tools.
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/mcp"
)

var safeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// TokenResolver returns the bearer token for a tool call.
type TokenResolver func(ctx context.Context, call tool.Call) (string, error)

type Server struct {
	Name              string
	DescriptionPrefix string
	Client            *mcp.Client
	ResolveToken      TokenResolver
	Effects           []tool.Effect
}

// ListDefinitions performs one MCP handshake and returns the remote tool
// definitions without registering them in a catalog. It is used by
// connection-scoped routers so a process-wide catalog never acquires
// user-specific tool names.
func ListDefinitions(ctx context.Context, client *mcp.Client) ([]mcp.ToolDefinition, error) {
	if client == nil {
		return nil, fmt.Errorf("mcp client is required")
	}
	session, err := client.Initialize(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListTools(ctx, session)
}

// Invoke calls one explicitly selected, read-only remote MCP tool without
// registering a per-user descriptor in the shared agent catalog. The remote
// definition is fetched in the same session immediately before the call and
// must opt into readOnlyHint=true. This keeps a generic connection router from
// turning an arbitrary remote MCP operation into a read-only catalog tool.
func Invoke(ctx context.Context, client *mcp.Client, name string, args json.RawMessage) (tool.Result, error) {
	return InvokeReadOnly(ctx, client, name, args)
}

// InvokeReadOnly validates the exact remote definition before invoking it.
// MCP annotations are untrusted hints, so absence or false is fail-closed.
// Dynamic write dispatch is intentionally not exposed by this router; writes
// must be represented by a separately declared, policy-reviewable tool.
func InvokeReadOnly(ctx context.Context, client *mcp.Client, name string, args json.RawMessage) (tool.Result, error) {
	if client == nil {
		return tool.Result{}, fmt.Errorf("mcp client is required")
	}
	session, err := client.Initialize(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	definitions, err := client.ListTools(ctx, session)
	if err != nil {
		return tool.Result{}, err
	}
	var selected mcp.ToolDefinition
	found := false
	for _, definition := range definitions {
		if definition.Name == name {
			selected = definition
			found = true
			break
		}
	}
	if !found {
		return remoteToolError("remote_tool_not_found", fmt.Sprintf("remote MCP tool %q was not advertised by this connection", name)), nil
	}
	if !explicitlyReadOnly(selected) {
		return remoteToolError("remote_tool_not_read_only", fmt.Sprintf("remote MCP tool %q is not explicitly read-only; dynamic writes are not available through this router", name)), nil
	}
	value, err := client.CallTool(ctx, session, name, args)
	if err != nil {
		return tool.Result{}, err
	}
	return formatToolResult(value), nil
}

// InvokeExternal validates and invokes one explicitly selected remote MCP
// tool through an external-write wrapper. The wrapper's descriptor must carry
// EffectExternalWrite so the composing server policy can require its exact
// stable wrapper name in the operator allowlist and route it through approval.
// A remote readOnlyHint=true definition is rejected here; reads belong on the
// read-only router and must not be used to smuggle a write through it.
func InvokeExternal(ctx context.Context, client *mcp.Client, name string, args json.RawMessage) (tool.Result, error) {
	if client == nil {
		return tool.Result{}, fmt.Errorf("mcp client is required")
	}
	session, err := client.Initialize(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	definitions, err := client.ListTools(ctx, session)
	if err != nil {
		return tool.Result{}, err
	}
	var selected mcp.ToolDefinition
	found := false
	for _, definition := range definitions {
		if definition.Name == name {
			selected = definition
			found = true
			break
		}
	}
	if !found {
		return remoteToolError("remote_tool_not_found", fmt.Sprintf("remote MCP tool %q was not advertised by this connection", name)), nil
	}
	if explicitlyReadOnly(selected) {
		return remoteToolError("remote_tool_read_only", fmt.Sprintf("remote MCP tool %q is read-only; use the read router", name)), nil
	}
	value, err := client.CallTool(ctx, session, name, args)
	if err != nil {
		return tool.Result{}, err
	}
	return formatToolResult(value), nil
}

func formatToolResult(value mcp.ToolResult) tool.Result {
	content := make([]model.Content, 0, 1+len(value.Images))
	if value.Content != "" {
		content = append(content, model.Content{Type: model.ContentText, Text: value.Content})
	}
	for _, image := range value.Images {
		content = append(content, model.Content{Type: model.ContentImage, ImageURL: image.DataURI()})
	}
	return tool.Result{Content: content, IsError: value.IsError, ErrorCode: mapErrorCode(value.IsError)}
}

func remoteToolError(code, message string) tool.Result {
	return tool.Result{
		Content:   []model.Content{{Type: model.ContentText, Text: message}},
		IsError:   true,
		ErrorCode: code,
	}
}

type remoteTool struct {
	server     *serverState
	remote     mcp.ToolDefinition
	descriptor tool.Descriptor
}
type serverState struct {
	config       clientConfig
	resolveToken TokenResolver
	mu           sync.Mutex
	sessions     map[string]sessionEntry
	initializing map[string]*sessionInitialization
}

type sessionEntry struct {
	session mcp.Session
	usedAt  time.Time
}
type sessionInitialization struct {
	done    chan struct{}
	session mcp.Session
	err     error
}

const maxCachedSessions = 256

// clientConfig intentionally contains only immutable transport configuration.
// A fresh mcp.Client owns its atomic request ID and sync.Once state per call.
type clientConfig struct {
	serviceName string
	url         string
	headers     map[string]string
	http        *http.Client
}

func Discover(ctx context.Context, config Server) ([]tool.Tool, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("mcp %s client is required", config.Name)
	}
	session, err := config.Client.Initialize(ctx)
	if err != nil {
		return nil, err
	}
	definitions, err := config.Client.ListTools(ctx, session)
	if err != nil {
		return nil, err
	}
	state := &serverState{
		config:       clientConfig{serviceName: config.Client.ServiceName, url: config.Client.URL, headers: cloneHeaders(config.Client.Headers), http: config.Client.HTTP},
		resolveToken: config.ResolveToken,
		sessions:     make(map[string]sessionEntry),
		initializing: make(map[string]*sessionInitialization),
	}
	items := make([]tool.Tool, 0, len(definitions))
	for _, definition := range definitions {
		name := "mcp_" + safeName.ReplaceAllString(config.Name, "_") + "_" + safeName.ReplaceAllString(definition.Name, "_")
		schema := definition.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		effects := append([]tool.Effect{tool.EffectNetwork}, config.Effects...)
		if !explicitlyReadOnly(definition) {
			effects = append(effects, tool.EffectExternalWrite)
		}
		description := definition.Description
		if prefix := strings.TrimSpace(config.DescriptionPrefix); prefix != "" {
			if description == "" {
				description = prefix
			} else {
				description = prefix + " " + description
			}
		}
		items = append(items, &remoteTool{server: state, remote: definition, descriptor: tool.Descriptor{Name: name, Description: description, InputSchema: schema, Effects: effects, Exposure: tool.ExposureDeferred}.WithConcurrencyDefaults()})
	}
	return items, nil
}

func explicitlyReadOnly(definition mcp.ToolDefinition) bool {
	return definition.Annotations != nil && definition.Annotations.ReadOnlyHint != nil && *definition.Annotations.ReadOnlyHint
}

func (t *remoteTool) Descriptor() tool.Descriptor { return t.descriptor }
func (t *remoteTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.server.resolveToken != nil {
		if _, err := t.server.resolveToken(ctx, call); err != nil {
			if result, convErr := connections.ToolResult(err); convErr == nil {
				return result, nil
			}
			return tool.Result{}, err
		}
	}
	session, err := t.server.session(ctx, call)
	if err != nil {
		return tool.Result{}, err
	}
	client := t.server.clientForCall(ctx, call)
	value, err := client.CallTool(ctx, session, t.remote.Name, call.Arguments)
	if err != nil {
		t.server.invalidateSession(call, session)
		return tool.Result{}, err
	}
	content := make([]model.Content, 0, 1+len(value.Images))
	if value.Content != "" {
		content = append(content, model.Content{Type: model.ContentText, Text: value.Content})
	}
	for _, image := range value.Images {
		content = append(content, model.Content{Type: model.ContentImage, ImageURL: image.DataURI()})
	}
	return tool.Result{Content: content, IsError: value.IsError, ErrorCode: mapErrorCode(value.IsError)}, nil
}

func mapErrorCode(isError bool) string {
	if isError {
		return "mcp_tool_error"
	}
	return ""
}

func (s *serverState) sessionKey(call tool.Call) string {
	userID := call.Scope.UserID
	if userID == "" {
		userID = "_"
	}
	return call.Scope.SessionID + "\x00" + userID
}

func (s *serverState) session(ctx context.Context, call tool.Call) (mcp.Session, error) {
	key := s.sessionKey(call)
	s.mu.Lock()
	if entry, ok := s.sessions[key]; ok {
		entry.usedAt = time.Now()
		s.sessions[key] = entry
		s.mu.Unlock()
		return entry.session, nil
	}
	if pending := s.initializing[key]; pending != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return mcp.Session{}, ctx.Err()
		case <-pending.done:
			return pending.session, pending.err
		}
	}
	pending := &sessionInitialization{done: make(chan struct{})}
	s.initializing[key] = pending
	s.mu.Unlock()
	client := s.clientForCall(ctx, call)
	session, err := client.Initialize(ctx)
	s.mu.Lock()
	delete(s.initializing, key)
	pending.session, pending.err = session, err
	if err == nil {
		s.sessions[key] = sessionEntry{session: session, usedAt: time.Now()}
		s.evictOldestLocked()
	}
	close(pending.done)
	s.mu.Unlock()
	return session, err
}

func (s *serverState) invalidateSession(call tool.Call, used mcp.Session) {
	key := s.sessionKey(call)
	s.mu.Lock()
	if current, ok := s.sessions[key]; ok && current.session == used {
		delete(s.sessions, key)
	}
	s.mu.Unlock()
}

func (s *serverState) evictOldestLocked() {
	for len(s.sessions) > maxCachedSessions {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.sessions {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey, oldest = key, entry.usedAt
			}
		}
		delete(s.sessions, oldestKey)
	}
}

func (s *serverState) clientForCall(ctx context.Context, call tool.Call) *mcp.Client {
	client := &mcp.Client{ServiceName: s.config.serviceName, URL: s.config.url, Headers: cloneHeaders(s.config.headers), HTTP: s.config.http}
	if s.resolveToken == nil {
		return client
	}
	token, err := s.resolveToken(ctx, call)
	if err == nil && token != "" {
		client.Token = token
	}
	return client
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	copy := make(map[string]string, len(headers))
	for key, value := range headers {
		copy[key] = value
	}
	return copy
}
