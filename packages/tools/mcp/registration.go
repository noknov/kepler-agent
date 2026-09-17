package mcptools

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/mcp"
)

// ErrCredentialRejected means MCP discovery failed because the stored bearer token
// was refused. Host surfaces should clear the integration connection and continue.
var ErrCredentialRejected = errors.New("mcp credential rejected")

// ClearConnection removes stored credentials for an integration after rejection.
type ClearConnection func(context.Context, string) error

// DiscoverForRegistration runs MCP discovery for lazy tool registration.
// Credential rejection is surfaced as ErrCredentialRejected without wrapping.
func DiscoverForRegistration(ctx context.Context, config Server) ([]tool.Tool, error) {
	items, err := Discover(ctx, config)
	if err == nil {
		return items, nil
	}
	if mcp.CredentialRejected(err) {
		return nil, ErrCredentialRejected
	}
	return nil, err
}

// DiscoverDeferredTools discovers MCP tools for lazy registration. Credential
// rejection clears the connection when clear is non-nil and returns nil so host
// turns are not blocked by an optional integration.
func DiscoverDeferredTools(ctx context.Context, integration, userID string, config Server, clear ClearConnection) ([]tool.Tool, error) {
	items, err := DiscoverForRegistration(ctx, config)
	if errors.Is(err, ErrCredentialRejected) {
		if clear != nil {
			if clearErr := clear(ctx, userID); clearErr != nil {
				log.Printf("%s: clear connection for user %s: %v", integration, userID, clearErr)
			}
		}
		log.Printf("%s: credential rejected for user %s, skipping deferred tools", integration, userID)
		return nil, nil
	}
	if err != nil {
		log.Printf("%s: discover failed for user %s: %v", integration, userID, err)
		return nil, fmt.Errorf("discover %s MCP tools: %w", integration, err)
	}
	return items, nil
}
