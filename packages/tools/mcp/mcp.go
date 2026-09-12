// Package mcptools adapts configured MCP servers into agent tools.
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
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
	Name         string
	Client       *mcp.Client
	ResolveToken TokenResolver
	Effects      []tool.Effect
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
		items = append(items, &remoteTool{server: state, remote: definition, descriptor: tool.Descriptor{Name: name, Description: definition.Description, InputSchema: schema, Effects: effects, Exposure: tool.ExposureDeferred}.WithConcurrencyDefaults()})
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
