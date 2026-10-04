package clickstack

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	agentmcp "github.com/noknov/kepler-agent/packages/mcp"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
)

// Registrar adds stable, connection-scoped ClickStack tools to a catalog.
// Catalogs are process-wide, so it deliberately never registers a descriptor
// containing a user's label, endpoint, or instance id. The selected instance
// is an explicit argument at execution time instead.
type Registrar struct {
	cfg  config.ClickStackConfig
	conn *connections.Service
	mu   sync.Mutex
	done bool
}

func NewRegistrar(cfg config.ClickStackConfig, conn *connections.Service) *Registrar {
	if !cfg.Enabled() && (conn == nil || !conn.Config.ClickStackEnabled()) {
		return nil
	}
	return &Registrar{cfg: cfg, conn: conn}
}

// Ensure registers the same provider-neutral router for every user. User
// connections are resolved only inside Execute, against the call's user id.
func (r *Registrar) Ensure(ctx context.Context, catalog *tool.Catalog, policy tool.SurfacePolicy, userID string) error {
	if r == nil || catalog == nil || r.conn == nil {
		return nil
	}
	_ = ctx
	_ = userID
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil
	}
	items := []tool.Tool{
		connectionListTool{conn: r.conn, cfg: r.cfg},
		connectionToolsTool{connectionCallTool: connectionCallTool{conn: r.conn, cfg: r.cfg}},
		connectionCallTool{conn: r.conn, cfg: r.cfg},
	}
	for _, item := range items {
		bound := tool.BindSurface(item, policy.Surface, "clickstack", "clickstack-connection")
		if err := catalog.RegisterDeferredVisible(policy, tool.CategoryInfrastructure, bound); err != nil {
			if errors.Is(err, tool.ErrAlreadyRegistered) {
				continue
			}
			return err
		}
	}
	r.done = true
	log.Printf("clickstack: registered stable connection router tools")
	return nil
}

// Invalidate is retained for connection-change hooks. Stable tools do not
// contain cached user descriptors, so the next Ensure only repairs a catalog
// that was created after the hook.
func (r *Registrar) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.done = false
	r.mu.Unlock()
}

func (r *Registrar) configForConnection(connection connections.Connection) config.ClickStackConfig {
	return configForConnection(r.cfg, connection)
}

// The resolver helper remains useful to integration callers and tests that
// need an instance-bound token without going through the generic router.
func (r *Registrar) tokenResolverForInstance(instanceID string) mcptools.TokenResolver {
	return func(ctx context.Context, call tool.Call) (string, error) {
		if r.conn == nil || r.conn.Store == nil || strings.TrimSpace(call.Scope.UserID) == "" {
			return "", connections.ErrNotConnected
		}
		token, err := r.conn.ClickStackAccessTokenInstance(ctx, call.Scope.UserID, instanceID)
		if err != nil {
			if errors.Is(err, connections.ErrNotConnected) {
				return "", r.conn.RequiredInstance(call.Scope.UserID, connections.ProviderClickStack, instanceID)
			}
			return "", err
		}
		return token, nil
	}
}

type connectionCallTool struct {
	conn          *connections.Service
	cfg           config.ClickStackConfig
	clientFactory func(connections.Connection, string) *agentmcp.Client
}

func (t connectionCallTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"clickstack_call",
		"Call a read-only ClickStack MCP tool using an instance_id from clickstack_connections and the exact tool name and input schema from clickstack_tools.",
		tool.ObjectSchema([]string{"instance_id", "tool_name"}, map[string]any{
			"instance_id": map[string]any{"type": "string", "description": "Opaque instance_id returned by clickstack_connections."},
			"tool_name":   map[string]any{"type": "string", "description": "Exact remote MCP tool name returned by clickstack_tools."},
			"arguments":   map[string]any{"type": "object", "description": "Arguments matching the selected remote MCP tool schema."},
		}),
		tool.ReadNetworkParallel("clickstack", "clickstack-connection")...,
	)
}

func (t connectionCallTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		InstanceID string          `json:"instance_id"`
		ToolName   string          `json:"tool_name"`
		Arguments  json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.InstanceID = strings.TrimSpace(args.InstanceID)
	args.ToolName = strings.TrimSpace(args.ToolName)
	if args.InstanceID == "" || args.ToolName == "" {
		return tool.Result{}, &connections.RequiredError{Provider: connections.ProviderClickStack, Title: "ClickStack connection and tool are required"}
	}
	client, unavailable, err := t.clientForInstance(ctx, call, args.InstanceID)
	if err != nil {
		return tool.Result{}, err
	}
	if unavailable != nil {
		return *unavailable, nil
	}
	if len(args.Arguments) == 0 {
		args.Arguments = json.RawMessage(`{}`)
	}
	return mcptools.InvokeReadOnly(ctx, client, args.ToolName, args.Arguments)
}

func (t connectionCallTool) clientForInstance(ctx context.Context, call tool.Call, instanceID string) (*agentmcp.Client, *tool.Result, error) {
	if t.conn == nil || t.conn.Store == nil {
		return nil, nil, connections.ErrNotConnected
	}
	connection, err := t.conn.GetConnection(ctx, call.Scope.UserID, connections.ProviderClickStack, instanceID)
	if errors.Is(err, connections.ErrNotConnected) {
		result := t.requiredResult(call.Scope.UserID, instanceID)
		return nil, &result, nil
	}
	if err != nil {
		return nil, nil, err
	}
	token, err := t.conn.ClickStackAccessTokenInstance(ctx, call.Scope.UserID, connection.InstanceID)
	var required *connections.RequiredError
	if errors.Is(err, connections.ErrNotConnected) || connections.AsRequired(err, &required) {
		result := t.requiredResult(call.Scope.UserID, instanceID)
		return nil, &result, nil
	}
	if err != nil {
		return nil, nil, err
	}
	client := NewMCPClient(t.configForConnection(connection), token)
	if t.clientFactory != nil {
		client = t.clientFactory(connection, token)
	}
	return client, nil, nil
}

func (t connectionCallTool) requiredResult(userID, instanceID string) tool.Result {
	result, err := connections.ToolResult(t.conn.RequiredInstance(userID, connections.ProviderClickStack, instanceID))
	if err != nil {
		return tool.TextResult(err.Error())
	}
	return result
}

func (t connectionCallTool) configForConnection(connection connections.Connection) config.ClickStackConfig {
	return configForConnection(t.cfg, connection)
}

func configForConnection(cfg config.ClickStackConfig, connection connections.Connection) config.ClickStackConfig {
	target := (connections.ClickStackOAuthConfig{MCPURL: cfg.MCPURL, ServiceID: cfg.ServiceID, TeamID: cfg.TeamID}).WithMetadata(connection.Metadata)
	return config.ClickStackConfig{MCPURL: target.MCPURL, ServiceID: target.ServiceID, TeamID: target.TeamID}
}

var _ tool.Tool = connectionListTool{}
var _ tool.Tool = connectionCallTool{}
var _ tool.Tool = connectionToolsTool{}
