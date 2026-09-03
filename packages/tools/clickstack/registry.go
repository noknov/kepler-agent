package clickstack

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
)

// Registrar lazily discovers ClickStack MCP tools once a user has connected.
type Registrar struct {
	cfg  config.ClickStackConfig
	conn *connections.Service
	mu   sync.Mutex
	done bool
}

func NewRegistrar(cfg config.ClickStackConfig, conn *connections.Service) *Registrar {
	if !cfg.Enabled() {
		return nil
	}
	return &Registrar{cfg: cfg, conn: conn}
}

// Ensure discovers and registers ClickStack tools for the given user.
func (r *Registrar) Ensure(ctx context.Context, catalog *tool.Catalog, policy tool.SurfacePolicy, userID string) error {
	if r == nil || catalog == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil
	}
	token, err := r.bootstrapToken(ctx, userID)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	items, err := mcptools.Discover(ctx, mcptools.Server{
		Name:         "clickstack",
		Client:       NewMCPClient(r.cfg, token),
		ResolveToken: r.tokenResolver(),
		Effects:      []tool.Effect{tool.EffectRead},
	})
	if err != nil {
		log.Printf("clickstack: discover failed for user %s: %v", userID, err)
		return fmt.Errorf("discover ClickStack MCP tools: %w", err)
	}
	for _, item := range items {
		bound := tool.BindSurface(item, policy.Surface, "clickstack", "clickstack-connection")
		if err := catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, bound); err != nil {
			if strings.Contains(err.Error(), "already registered") {
				continue
			}
			return err
		}
	}
	r.done = true
	log.Printf("clickstack: registered %d MCP tools", len(items))
	return nil
}

// Invalidate clears lazy-registration state so the next Ensure re-discovers tools.
// Call after a user connects or reconnects an integration.
func (r *Registrar) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.done = false
	r.mu.Unlock()
}

func (r *Registrar) bootstrapToken(ctx context.Context, userID string) (string, error) {
	if r.conn == nil || strings.TrimSpace(userID) == "" {
		return "", nil
	}
	token, err := r.conn.ClickStackAccessToken(ctx, userID)
	if err == connections.ErrNotConnected {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func (r *Registrar) tokenResolver() mcptools.TokenResolver {
	return func(ctx context.Context, call tool.Call) (string, error) {
		if r.conn == nil || r.conn.Store == nil {
			return "", connections.ErrNotConnected
		}
		if strings.TrimSpace(call.Scope.UserID) == "" {
			return "", connections.ErrNotConnected
		}
		token, err := r.conn.ClickStackAccessToken(ctx, call.Scope.UserID)
		if err != nil {
			if err == connections.ErrNotConnected {
				return "", r.conn.Required(call.Scope.UserID, connections.ProviderClickStack)
			}
			return "", err
		}
		return token, nil
	}
}
