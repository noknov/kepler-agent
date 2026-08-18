package clickstack

import (
	"context"
	"strings"
	"sync"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	mcptools "github.com/noknov/slack-copilot-agent/packages/tools/mcp"
)

// Registrar lazily discovers ClickStack MCP tools once a usable token exists.
type Registrar struct {
	cfg    config.ClickStackConfig
	conn   *connections.Service
	mu     sync.Mutex
	done   bool
}

func NewRegistrar(cfg config.ClickStackConfig, conn *connections.Service) *Registrar {
	if !cfg.Enabled() {
		return nil
	}
	return &Registrar{cfg: cfg, conn: conn}
}

// Ensure discovers and registers ClickStack tools into the catalog when needed.
func (r *Registrar) Ensure(ctx context.Context, catalog *tool.Catalog, policy tool.SurfacePolicy, userID string) error {
	if r == nil || catalog == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil
	}
	token := bootstrapToken(ctx, r.conn, userID)
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
		return err
	}
	for _, item := range items {
		bound := tool.BindSurface(item, policy.Surface, "clickstack")
		if err := catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, bound); err != nil {
			if strings.Contains(err.Error(), "already registered") {
				continue
			}
			return err
		}
	}
	r.done = true
	return nil
}

func (r *Registrar) tokenResolver() mcptools.TokenResolver {
	return func(ctx context.Context, call tool.Call) (string, error) {
		if r.conn == nil || r.conn.Store == nil {
			return "", connections.ErrNotConnected
		}
		if strings.TrimSpace(call.Scope.UserID) == "" {
			return "", connections.ErrNotConnected
		}
		token, err := r.conn.Store.Token(ctx, call.Scope.UserID, connections.ProviderClickStack)
		if err != nil {
			if err == connections.ErrNotConnected {
				return "", r.conn.Required(call.Scope.UserID, connections.ProviderClickStack)
			}
			return "", err
		}
		return token, nil
	}
}

func bootstrapToken(ctx context.Context, conn *connections.Service, userID string) string {
	if conn != nil && conn.Store != nil {
		if userID != "" {
			if token, err := conn.Store.Token(ctx, userID, connections.ProviderClickStack); err == nil && token != "" {
				return token
			}
		}
		if token, err := conn.Store.AnyToken(ctx, connections.ProviderClickStack); err == nil && token != "" {
			return token
		}
	}
	return ""
}
