// Package mcptools adapts configured MCP servers into agent tools.
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
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
	template     mcp.Client
	resolveToken TokenResolver
	mu           sync.Mutex
	sessions     map[string]mcp.Session
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
	template := *config.Client
	template.Token = ""
	state := &serverState{
		template:     template,
		resolveToken: config.ResolveToken,
		sessions:     make(map[string]mcp.Session),
	}
	items := make([]tool.Tool, 0, len(definitions))
	for _, definition := range definitions {
		name := "mcp_" + safeName.ReplaceAllString(config.Name, "_") + "_" + safeName.ReplaceAllString(definition.Name, "_")
		schema := definition.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		effects := append([]tool.Effect{tool.EffectNetwork}, config.Effects...)
		items = append(items, &remoteTool{server: state, remote: definition, descriptor: tool.Descriptor{Name: name, Description: definition.Description, InputSchema: schema, Effects: effects, Exposure: tool.ExposureDeferred}})
	}
	return items, nil
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
	defer s.mu.Unlock()
	if session, ok := s.sessions[key]; ok {
		return session, nil
	}
	client := s.clientForCall(ctx, call)
	session, err := client.Initialize(ctx)
	if err != nil {
		return mcp.Session{}, err
	}
	s.sessions[key] = session
	return session, nil
}

func (s *serverState) clientForCall(ctx context.Context, call tool.Call) *mcp.Client {
	client := s.template
	if s.resolveToken == nil {
		return &client
	}
	token, err := s.resolveToken(ctx, call)
	if err == nil && token != "" {
		client.Token = token
	}
	return &client
}
