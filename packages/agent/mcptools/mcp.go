// Package mcptools adapts configured MCP servers into agent tools.
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/mcp"
)

var safeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

type Server struct {
	Name    string
	Client  *mcp.Client
	Effects []tool.Effect
}
type remoteTool struct {
	server     *serverState
	remote     mcp.ToolDefinition
	descriptor tool.Descriptor
}
type serverState struct {
	client    *mcp.Client
	mu        sync.Mutex
	sessions  map[string]mcp.Session
	bootstrap *mcp.Session
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
	state := &serverState{client: config.Client, sessions: make(map[string]mcp.Session), bootstrap: &session}
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
	session, err := t.server.session(ctx, call.Scope.SessionID)
	if err != nil {
		return tool.Result{}, err
	}
	value, err := t.server.client.CallTool(ctx, session, t.remote.Name, call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	isError := strings.HasPrefix(value, "[tool error] ")
	return tool.Result{Content: []model.Content{{Type: model.ContentText, Text: value}}, IsError: isError, ErrorCode: mapErrorCode(isError)}, nil
}

func mapErrorCode(isError bool) string {
	if isError {
		return "mcp_tool_error"
	}
	return ""
}

func (s *serverState) session(ctx context.Context, agentSession string) (mcp.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[agentSession]; ok {
		return session, nil
	}
	if s.bootstrap != nil {
		session := *s.bootstrap
		s.bootstrap = nil
		s.sessions[agentSession] = session
		return session, nil
	}
	session, err := s.client.Initialize(ctx)
	if err != nil {
		return mcp.Session{}, err
	}
	s.sessions[agentSession] = session
	return session, nil
}
