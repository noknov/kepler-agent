package clickstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	agentmcp "github.com/noknov/kepler-agent/packages/mcp"
	mcptools "github.com/noknov/kepler-agent/packages/tools/mcp"
)

type connectionListTool struct {
	conn *connections.Service
	cfg  config.ClickStackConfig
}

func (t connectionListTool) configForConnection(connection connections.Connection) config.ClickStackConfig {
	return configForConnection(t.cfg, connection)
}

func (t connectionListTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"clickstack_connections",
		"List all saved ClickStack connection instances for the current user, including unnamed connections and their resolved targets. Status is stored credential state, not a live access check. Use instance_id to select a connection; discover its tools with clickstack_tools.",
		tool.ObjectSchema(nil, nil),
		tool.WithEffects(tool.EffectRead),
		tool.WithParallel(true),
		tool.WithDependencies("clickstack", "clickstack-connection"),
	)
}

func (t connectionListTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.conn == nil || t.conn.Store == nil {
		return tool.TextResult("ClickStack connections are not configured."), nil
	}
	items, err := t.conn.ListConnections(ctx, call.Scope.UserID)
	if err != nil && !errors.Is(err, connections.ErrNotConnected) {
		return tool.Result{}, err
	}
	result := make([]connectionSummary, 0)
	for _, connection := range items {
		if connection.Provider != connections.ProviderClickStack {
			continue
		}
		cfg := t.configForConnection(connection)
		endpoint := strings.TrimSpace(cfg.MCPURL)
		if endpoint == "" {
			endpoint = defaultMCPURL
		}
		name := strings.TrimSpace(connection.Label)
		if name == "" {
			name = "ClickStack"
		}
		origin := "deployment_config"
		for _, key := range []string{"mcp_url", "service_id", "team_id"} {
			if strings.TrimSpace(connection.Metadata[key]) != "" {
				origin = "connection"
				break
			}
		}
		result = append(result, connectionSummary{
			InstanceID: connections.NormalizeInstanceID(connection.InstanceID),
			Label:      connection.Label, DisplayName: name, Status: connection.Status,
			Endpoint: endpoint, ServiceID: cfg.ServiceID, TeamID: cfg.TeamID, TargetOrigin: origin,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstanceID < result[j].InstanceID })
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
	data, err := json.Marshal(struct {
		Total       int                 `json:"total"`
		Connections []connectionSummary `json:"connections"`
	}{Total: len(result), Connections: result})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}

type connectionSummary struct {
	InstanceID   string             `json:"instance_id"`
	Label        string             `json:"label"`
	DisplayName  string             `json:"display_name"`
	Status       connections.Status `json:"status"`
	Endpoint     string             `json:"endpoint"`
	ServiceID    string             `json:"service_id,omitempty"`
	TeamID       string             `json:"team_id,omitempty"`
	TargetOrigin string             `json:"target_origin"`
}

// Discovery is separate from inventory: remote schemas cannot bury another
// connection, and fetching one tool never uploads every other tool's schema.
type connectionToolsTool struct{ connectionCallTool }

func (t connectionToolsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("clickstack_tools",
		"Discover remote ClickStack tools for one connection. Without tool_name, returns a paginated name/read-only catalog. Supply tool_name to retrieve that tool's full description and input schema.",
		tool.ObjectSchema([]string{"instance_id"}, map[string]any{
			"instance_id": map[string]any{"type": "string", "description": "Opaque instance_id returned by clickstack_connections."},
			"tool_name":   map[string]any{"type": "string", "description": "Exact tool name to inspect; omit to list names."},
			"offset":      map[string]any{"type": "integer", "minimum": 0},
			"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50},
		}), tool.ReadNetworkParallel("clickstack", "clickstack-connection")...)
}

func (t connectionToolsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		InstanceID string `json:"instance_id"`
		ToolName   string `json:"tool_name"`
		Offset     int    `json:"offset"`
		Limit      *int   `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.InstanceID = strings.TrimSpace(args.InstanceID)
	if args.InstanceID == "" {
		return tool.Result{}, fmt.Errorf("instance_id is required")
	}
	limit := 50
	if args.Limit != nil {
		limit = *args.Limit
	}
	if args.Offset < 0 || limit < 1 || limit > 100 {
		return tool.Result{}, fmt.Errorf("offset must be nonnegative and limit must be between 1 and 100")
	}
	client, unavailable, err := t.clientForInstance(ctx, call, args.InstanceID)
	if err != nil {
		return tool.Result{}, err
	}
	if unavailable != nil {
		return *unavailable, nil
	}
	definitions, err := mcptools.ListDefinitions(ctx, client)
	if err != nil {
		return tool.Result{}, err
	}
	if args.ToolName != "" {
		for _, definition := range definitions {
			if definition.Name == args.ToolName {
				data, err := json.Marshal(struct {
					InstanceID string                  `json:"instance_id"`
					Tool       agentmcp.ToolDefinition `json:"tool"`
				}{args.InstanceID, definition})
				if err != nil {
					return tool.Result{}, err
				}
				return tool.TextResult(string(data)), nil
			}
		}
		result := tool.TextResult(fmt.Sprintf("Tool %q was not advertised by this connection.", args.ToolName))
		result.IsError, result.ErrorCode = true, "remote_tool_not_found"
		return result, nil
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	type summary struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	}
	items := make([]summary, 0)
	end := len(definitions)
	// The offset is checked before addition so a very large offset cannot overflow.
	if args.Offset < len(definitions) {
		end = args.Offset + min(limit, len(definitions)-args.Offset)
		for _, definition := range definitions[args.Offset:end] {
			readOnly := definition.Annotations != nil && definition.Annotations.ReadOnlyHint != nil && *definition.Annotations.ReadOnlyHint
			items = append(items, summary{Name: definition.Name, ReadOnly: readOnly})
		}
	}
	var next *int
	if end < len(definitions) {
		next = &end
	}
	data, err := json.Marshal(struct {
		InstanceID string    `json:"instance_id"`
		Total      int       `json:"total"`
		Tools      []summary `json:"tools"`
		NextOffset *int      `json:"next_offset,omitempty"`
	}{args.InstanceID, len(definitions), items, next})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(string(data)), nil
}
