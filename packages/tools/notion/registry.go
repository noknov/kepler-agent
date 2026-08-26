package notion

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

// Registrar lazily discovers Notion MCP tools once a user has connected.
type Registrar struct {
	cfg  config.NotionConfig
	conn *connections.Service
	mu   sync.Mutex
	done bool
}

func NewRegistrar(cfg config.NotionConfig, conn *connections.Service) *Registrar {
	if !cfg.Enabled() {
		return nil
	}
	return &Registrar{cfg: cfg, conn: conn}
}

// Ensure discovers and registers Notion MCP tools for the given user.
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
		Name:         "notion",
		Client:       NewMCPClient(r.cfg, token),
		ResolveToken: r.tokenResolver(),
		Effects:      []tool.Effect{tool.EffectRead},
	})
	if err != nil {
		log.Printf("notion: discover failed for user %s: %v", userID, err)
		return fmt.Errorf("discover Notion MCP tools: %w", err)
	}
	for _, item := range items {
		bound := tool.BindSurface(item, policy.Surface, "notion", "notion-connection")
		if err := catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, bound); err != nil {
			if strings.Contains(err.Error(), "already registered") {
				continue
			}
			return err
		}
	}
	r.done = true
	log.Printf("notion: registered %d MCP tools", len(items))
	return nil
}

func (r *Registrar) bootstrapToken(ctx context.Context, userID string) (string, error) {
	if r.conn == nil || strings.TrimSpace(userID) == "" {
		return "", nil
	}
	token, err := r.conn.NotionAccessToken(ctx, userID)
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
		token, err := r.conn.NotionAccessToken(ctx, call.Scope.UserID)
		if err != nil {
			if err == connections.ErrNotConnected {
				return "", r.conn.Required(call.Scope.UserID, connections.ProviderNotion)
			}
			return "", err
		}
		return token, nil
	}
}
