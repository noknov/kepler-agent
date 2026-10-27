package slacktool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type UserListItemsTool struct{ Source ClientSource }

func (UserListItemsTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-user_list_items", "Read one Slack List page and its column schema as the connected user. Use returned column IDs and native field types for later explicit writes; never guess columns from names. Fetch another page only when needed.", tool.ObjectSchema([]string{"list_id"}, map[string]any{
		"list_id": map[string]any{"type": "string", "minLength": 1}, "cursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": slack.MaxListItemsPerPage},
	}), readNetwork("slack-connection")...)
}
func (t UserListItemsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var in slack.ListItemsInput
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
	page, err := client.ListItems(ctx, in)
	if err != nil {
		return tool.Result{}, err
	}
	result, err := structuredSlackResult(page)
	result.Truncated = page.NextCursor != ""
	return result, err
}

type UserCreateListItemTool struct{ Source ClientSource }

func (UserCreateListItemTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-user_create_list_item", "Create exactly one item in an explicitly selected Slack List as the connected user, only after the user requests it. Read the List's column schema first. initial_fields use native Slack column_id/value objects (rich_text, user, date, select, checkbox, number, email, phone, attachment, link, message, rating, timestamp, channel or reference). Preserve evidence as a reference field when the schema supports it; never infer owners/deadlines. An uncertain result must be reconciled by reading the List, not retried.", tool.ObjectSchema([]string{"list_id", "initial_fields"}, map[string]any{
		"list_id": map[string]any{"type": "string", "minLength": 1}, "parent_item_id": map[string]any{"type": "string"}, "initial_fields": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "required": []string{"column_id"}, "properties": map[string]any{"column_id": map[string]any{"type": "string", "minLength": 1}}, "additionalProperties": true}},
	}), externalWrite("slack-connection")...)
}
func (t UserCreateListItemTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var in slack.CreateListItemInput
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
	item, err := client.CreateListItem(ctx, in)
	if err != nil {
		return slackMutationFailure(err)
	}
	return structuredSlackResult(map[string]any{"list_id": in.ListID, "item": item})
}

func structuredSlackResult(value any) (tool.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: []model.Content{{Type: model.ContentJSON, JSON: data}}}, nil
}

func slackMutationFailure(err error) (tool.Result, error) {
	var uncertain interface{ DeliveryUncertain() bool }
	if !errors.As(err, &uncertain) || !uncertain.DeliveryUncertain() {
		return tool.Result{}, err
	}
	result := tool.TextResult("Slack did not confirm this write. Its outcome is uncertain; check the destination before any repeat.")
	result.IsError = true
	result.ErrorCode = "uncertain_delivery"
	result.Metadata = map[string]any{"delivery": "uncertain"}
	return result, nil
}
