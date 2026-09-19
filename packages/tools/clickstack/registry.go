package clickstack

import (
	"context"
	"encoding/json"
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
		connectionCallTool{conn: r.conn, cfg: r.cfg},
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
	cfg := r.cfg
	if value := strings.TrimSpace(connection.Metadata["mcp_url"]); value != "" {
		cfg.MCPURL = value
	}
	if value := strings.TrimSpace(connection.Metadata["service_id"]); value != "" {
		cfg.ServiceID = value
	}
	return cfg
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
			if err == connections.ErrNotConnected {
				return "", r.conn.RequiredInstance(call.Scope.UserID, connections.ProviderClickStack, instanceID)
			}
			return "", err
		}
		return token, nil
	}
}

type connectionListTool struct {
	conn *connections.Service
	cfg  config.ClickStackConfig
}

func (t connectionListTool) configForConnection(connection connections.Connection) config.ClickStackConfig {
	cfg := t.cfg
	if value := strings.TrimSpace(connection.Metadata["mcp_url"]); value != "" {
		cfg.MCPURL = value
	}
	if value := strings.TrimSpace(connection.Metadata["service_id"]); value != "" {
		cfg.ServiceID = value
	}
	return cfg
}

func (t connectionListTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"clickstack_connections",
		"List the ClickStack connections available to the current user and the remote MCP tools exposed by each one. Use the returned instance_id explicitly when calling clickstack_call.",
		tool.ObjectSchema(nil, nil),
		tool.ReadNetworkParallel("clickstack", "clickstack-connection")...,
	)
}

func (t connectionListTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.conn == nil || t.conn.Store == nil {
		return tool.TextResult("ClickStack connections are not configured."), nil
	}
	items, err := t.conn.ListConnections(ctx, call.Scope.UserID)
	if err != nil && err != connections.ErrNotConnected {
		return tool.Result{}, err
	}
	result := make([]map[string]any, 0)
	for _, connection := range items {
		if connection.Provider != connections.ProviderClickStack {
			continue
		}
		instanceID := connections.NormalizeInstanceID(connection.InstanceID)
		entry := map[string]any{
			"instance_id": instanceID,
			"label":       connections.InstanceDisplayName(connection),
			"account":     connection.Account,
			"status":      connection.Status,
		}
		if rawURL := strings.TrimSpace(connection.Metadata["mcp_url"]); rawURL != "" {
			entry["endpoint"] = rawURL
		}
		token, tokenErr := t.conn.ClickStackAccessTokenInstance(ctx, call.Scope.UserID, instanceID)
		if tokenErr != nil {
			entry["status"] = "reauthorization_required"
			result = append(result, entry)
			continue
		}
		definitions, listErr := mcptools.ListDefinitions(ctx, NewMCPClient(t.configForConnection(connection), token))
		if listErr != nil {
			entry["tools_error"] = listErr.Error()
		} else {
			entry["tools"] = definitions
		}
		result = append(result, entry)
	}
	if len(result) == 0 {
		requiredErr := t.conn.Required(call.Scope.UserID, connections.ProviderClickStack)
		if requiredErr == nil {
			return tool.TextResult("No ClickStack connections are available."), nil
		}
		var required *connections.RequiredError
		if !connections.AsRequired(requiredErr, &required) {
			return tool.Result{}, requiredErr
		}
		// Empty-list discovery is the chat equivalent of clicking “＋ Add
		// integration”. Keep the action URL empty so Slack opens the provider's
		// declared details modal rather than skipping custom fields.
		required.AuthURL = ""
		connectionResult, resultErr := connections.ToolResult(required)
		if resultErr != nil {
			return tool.Result{}, resultErr
		}
		return connectionResult, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

type connectionCallTool struct {
	conn          *connections.Service
	cfg           config.ClickStackConfig
	clientFactory func(connections.Connection, string) *agentmcp.Client
}

func (t connectionCallTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"clickstack_call",
		"Call a ClickStack MCP tool for an explicitly selected connection. First call clickstack_connections to discover instance_id, tool_name, and input schema; never substitute a label for instance_id.",
		tool.ObjectSchema([]string{"instance_id", "tool_name"}, map[string]any{
			"instance_id": map[string]any{"type": "string", "description": "Opaque instance_id returned by clickstack_connections."},
			"tool_name":   map[string]any{"type": "string", "description": "Exact remote MCP tool name returned by clickstack_connections."},
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
	if t.conn == nil || t.conn.Store == nil {
		return tool.TextResult("ClickStack connections are not configured."), nil
	}
	connection, err := t.conn.GetConnection(ctx, call.Scope.UserID, connections.ProviderClickStack, args.InstanceID)
	if err != nil {
		return t.requiredResult(call.Scope.UserID, args.InstanceID), nil
	}
	token, err := t.conn.ClickStackAccessTokenInstance(ctx, call.Scope.UserID, connection.InstanceID)
	if err != nil {
		return t.requiredResult(call.Scope.UserID, connection.InstanceID), nil
	}
	if len(args.Arguments) == 0 {
		args.Arguments = json.RawMessage(`{}`)
	}
	client := NewMCPClient(t.configForConnection(connection), token)
	if t.clientFactory != nil {
		client = t.clientFactory(connection, token)
	}
	return mcptools.InvokeReadOnly(ctx, client, args.ToolName, args.Arguments)
}

func (t connectionCallTool) requiredResult(userID, instanceID string) tool.Result {
	result, err := connections.ToolResult(t.conn.RequiredInstance(userID, connections.ProviderClickStack, instanceID))
	if err != nil {
		return tool.TextResult(err.Error())
	}
	return result
}

func (t connectionCallTool) configForConnection(connection connections.Connection) config.ClickStackConfig {
	cfg := t.cfg
	if value := strings.TrimSpace(connection.Metadata["mcp_url"]); value != "" {
		cfg.MCPURL = value
	}
	if value := strings.TrimSpace(connection.Metadata["service_id"]); value != "" {
		cfg.ServiceID = value
	}
	return cfg
}

var _ tool.Tool = connectionListTool{}
var _ tool.Tool = connectionCallTool{}
