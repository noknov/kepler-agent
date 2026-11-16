package slacktool

import (
	"context"
	"encoding/json"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
)

// AddTaskSpaceTools is invoked only for an operator-enabled native installation.
// Runtime capability policy still requires each external-write tool's exact allowlist entry.
func AddTaskSpaceTools(catalog *tool.Catalog, policy tool.SurfacePolicy, spaces *taskspace.Service) error {
	if spaces == nil {
		return nil
	}
	registration := tool.NewRegistration(catalog, policy)
	registration.Visible(CreateTaskSpaceTool{Spaces: spaces})
	registration.Deferred(tool.CategoryIntegration, ListTaskViewsTool{Spaces: spaces})
	registration.Deferred(tool.CategoryIntegration, ReadTaskCanvasTool{Spaces: spaces})
	registration.Deferred(tool.CategoryIntegration, UpdateTaskCanvasTool{Spaces: spaces})
	registration.Deferred(tool.CategoryIntegration, RemoveTaskViewTool{Spaces: spaces})
	registration.Deferred(tool.CategoryIntegration, SetTaskContextTool{Spaces: spaces})
	registration.Deferred(tool.CategoryIntegration, SetTaskViewTool{Spaces: spaces})
	return registration.Err()
}

type CreateTaskSpaceTool struct{ Spaces *taskspace.Service }

func (t CreateTaskSpaceTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-create_task_space", "Create a native Slack task channel for multi-step work. The authenticated current message supplies its origin and privacy. The current turn finishes here; continue in the linked channel on the user's next explicit mention. Do not create nested task spaces.", tool.ObjectSchema([]string{"brief"}, map[string]any{"name": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string", "maxLength": 4000, "description": "A concise task brief for this channel, including goal, constraints and current findings. Do not copy unrelated conversation content."}}), externalWrite()...)
}
func (t CreateTaskSpaceTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Name  string `json:"name"`
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	space, err := t.Spaces.Create(ctx, call.Scope, args.Name, args.Brief)
	if err != nil {
		return tool.Result{}, err
	}
	payload, _ := json.Marshal(map[string]string{"channel_id": space.Channel, "origin_channel_id": space.OriginChannel, "origin_message_ts": space.OriginMessageTS})
	return tool.TextResult(string(payload)), nil
}

type SetTaskContextTool struct{ Spaces *taskspace.Service }

func (t SetTaskContextTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-set_task_context", "Replace the current native task channel's context bar with up to five informational links, such as repository, branch and PR. Include all items to retain.", tool.ObjectSchema([]string{"items"}, map[string]any{"items": map[string]any{"type": "array", "maxItems": 5, "items": map[string]any{"type": "object", "required": []string{"key", "label"}, "properties": map[string]any{"key": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "icon": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}}, "additionalProperties": false}}}), externalWrite()...)
}
func (t SetTaskContextTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Items []slack.CodeContextItem `json:"items"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	channel, err := t.Spaces.Current(ctx, call.Scope)
	if err != nil {
		return tool.Result{}, err
	}
	if err := t.Spaces.Slack.SetCodeContext(ctx, channel, args.Items); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult("Task context updated."), nil
}

type SetTaskViewTool struct{ Spaces *taskspace.Service }

func (t SetTaskViewTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-set_task_view", "Create or update a view in the current native task channel. HTML must be self-contained. Use a stable view_key; diff is a channel singleton. Canvas attachment grants comment access.", tool.ObjectSchema([]string{"type"}, map[string]any{
		"type":     map[string]any{"type": "string", "enum": []string{"html", "diff", "block_kit", "canvas"}},
		"view_key": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
		"blocks":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		"canvas_id": map[string]any{"type": "string"}, "base_branch": map[string]any{"type": "string"}, "head_branch": map[string]any{"type": "string"},
	}), externalWrite()...)
}
func (t SetTaskViewTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var view slack.CodeView
	if err := json.Unmarshal(call.Arguments, &view); err != nil {
		return tool.Result{}, err
	}
	channel, err := t.Spaces.Current(ctx, call.Scope)
	if err != nil {
		return tool.Result{}, err
	}
	view.Channel = channel // Model arguments never select a write destination.
	if err := t.Spaces.Slack.SetCodeView(ctx, view); err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult("Task view updated."), nil
}
