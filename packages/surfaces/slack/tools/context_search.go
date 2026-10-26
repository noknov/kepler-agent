package slacktool

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

// UserSearchTool discovers message evidence using the requesting user's linked
// identity. Catalog wiring never supplies a bot fallback for workspace searches.
type UserSearchTool struct{ Source ClientSource }

func (UserSearchTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-user_search", "Search Slack messages as the connected user. Use a focused natural-language question or Slack query filters to discover relevant discussions, then read a specific thread only if needed. Returns source permalinks and nearby messages. Default channel type is public_channel; request private_channel, im or mpim explicitly when relevant and authorized. Semantic retrieval depends on the workspace plan. Fetch another page only when needed; each call uses Slack's interactive search quota.", tool.ObjectSchema([]string{"query"}, map[string]any{
		"query":              map[string]any{"type": "string", "minLength": 1},
		"channel_types":      map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"public_channel", "private_channel", "im", "mpim"}}},
		"context_channel_id": map[string]any{"type": "string", "description": "Optional known channel to focus the search."},
		"cursor":             map[string]any{"type": "string", "description": "Optional next_cursor from the prior result."},
		"limit":              map[string]any{"type": "integer", "minimum": 1, "maximum": slack.MaxContextSearchResults},
	}), readNetwork("slack-connection")...)
}

func (t UserSearchTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var in slack.ContextSearchInput
	if err := json.Unmarshal(call.Arguments, &in); err != nil {
		return tool.Result{}, err
	}
	if err := in.Validate(); err != nil {
		return tool.Result{}, err
	}
	client, early, err := beginClientAccess(ctx, t.Source, call)
	if early != nil {
		return *early, err
	}
	if err != nil {
		return tool.Result{}, err
	}
	result, err := client.SearchContext(ctx, in)
	if err != nil {
		return tool.Result{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: []model.Content{{Type: model.ContentJSON, JSON: data}}}, nil
}
