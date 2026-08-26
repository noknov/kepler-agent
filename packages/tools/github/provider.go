package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
)

// ClientSource resolves a GitHub API client for a tool call.
type ClientSource interface {
	Resolve(ctx context.Context, call tool.Call) (Client, error)
}

// StaticSource uses a single operator token.
type StaticSource struct{ API Client }

func (s StaticSource) Resolve(_ context.Context, _ tool.Call) (Client, error) {
	if s.API.enabled() {
		return s.API, nil
	}
	return Client{}, errors.New("github is not configured")
}

// ConnectedSource uses per-user OAuth tokens when GitHub OAuth is enabled.
type ConnectedSource struct {
	Service  connections.Service
	Defaults Client
}

func (s ConnectedSource) Resolve(ctx context.Context, call tool.Call) (Client, error) {
	if s.Service.Store == nil {
		return Client{}, errors.New("github connections are not configured")
	}
	if call.Scope.UserID == "" {
		return Client{}, errors.New("user id is required for github")
	}
	token, err := s.Service.Store.Token(ctx, call.Scope.UserID, connections.ProviderGitHub)
	if err != nil {
		if errors.Is(err, connections.ErrNotConnected) {
			return Client{}, s.Service.Required(call.Scope.UserID, connections.ProviderGitHub)
		}
		return Client{}, err
	}
	client := s.Defaults
	client.Token = token
	return client, nil
}

func begin(ctx context.Context, source ClientSource, fallback Client, call tool.Call) (Client, *tool.Result, error) {
	if source == nil {
		if !fallback.enabled() {
			return Client{}, nil, fmt.Errorf("GitHub is not configured: GITHUB_TOKEN is required")
		}
		return fallback, nil, nil
	}
	client, err := source.Resolve(ctx, call)
	if err != nil {
		if result, convErr := toolResult(err); convErr == nil {
			return Client{}, &result, nil
		}
		return Client{}, nil, err
	}
	if !client.enabled() {
		if result, convErr := toolResult(errors.New("github not connected")); convErr == nil {
			return Client{}, &result, nil
		}
		return Client{}, nil, errors.New("github is not configured")
	}
	return client, nil, nil
}

func toolResult(err error) (tool.Result, error) {
	if err == nil {
		return tool.Result{}, nil
	}
	if result, convErr := connections.ToolResult(err); convErr == nil {
		return result, nil
	}
	return tool.Result{}, err
}
