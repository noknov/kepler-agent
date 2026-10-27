package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const MaxListItemsPerPage = 100

type ListItemsInput struct {
	ListID string `json:"list_id"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (in ListItemsInput) Validate() error {
	if strings.TrimSpace(in.ListID) == "" {
		return fmt.Errorf("list_id is required")
	}
	if in.Limit < 0 || in.Limit > MaxListItemsPerPage {
		return fmt.Errorf("list item limit must be 1..%d or omitted", MaxListItemsPerPage)
	}
	return nil
}

type ListItemsPage struct {
	ListID     string            `json:"list_id"`
	List       json.RawMessage   `json:"list,omitempty"`
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// ListItems reads one page and its native column schema without inferring names.
func (c *Client) ListItems(ctx context.Context, in ListItemsInput) (ListItemsPage, error) {
	if err := in.Validate(); err != nil {
		return ListItemsPage{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	var out struct {
		OK       bool              `json:"ok"`
		Error    string            `json:"error"`
		List     json.RawMessage   `json:"list"`
		Items    []json.RawMessage `json:"items"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	payload := map[string]any{"list_id": in.ListID, "include_list": true, "limit": limit}
	if in.Cursor != "" {
		payload["cursor"] = in.Cursor
	}
	if err := c.postJSON(ctx, "slackLists.items.list", payload, &out); err != nil {
		return ListItemsPage{}, err
	}
	if !out.OK {
		return ListItemsPage{}, slackAPIError{Method: "slackLists.items.list", Code: out.Error}
	}
	if len(out.Items) > limit {
		return ListItemsPage{}, fmt.Errorf("Slack list page exceeds requested item limit")
	}
	next, err := checkedPaginationCursor("slackLists.items.list", out.Metadata.NextCursor, map[string]bool{strings.TrimSpace(in.Cursor): true})
	if err != nil {
		return ListItemsPage{}, err
	}
	return ListItemsPage{ListID: in.ListID, List: out.List, Items: out.Items, NextCursor: next}, nil
}

// ListField uses Slack's native column-ID/value representation. Values stay raw
// JSON so rich text, references and numeric cells are not guessed or rounded.
type ListField map[string]json.RawMessage

func (field ListField) validate() error {
	var column string
	if json.Unmarshal(field["column_id"], &column) != nil || strings.TrimSpace(column) == "" || len(field) != 2 {
		return fmt.Errorf("each list field requires column_id and exactly one native value")
	}
	for key, value := range field {
		switch key {
		case "column_id", "rich_text", "user", "date", "select", "checkbox", "number", "email", "phone", "attachment", "link", "message", "rating", "timestamp", "channel", "reference":
			if !json.Valid(value) {
				return fmt.Errorf("invalid list field JSON")
			}
		default:
			return fmt.Errorf("unsupported native list field type")
		}
	}
	return nil
}

type CreateListItemInput struct {
	ListID        string      `json:"list_id"`
	InitialFields []ListField `json:"initial_fields"`
	ParentItemID  string      `json:"parent_item_id,omitempty"`
}

func (in CreateListItemInput) Validate() error {
	if strings.TrimSpace(in.ListID) == "" || len(in.InitialFields) == 0 {
		return fmt.Errorf("list_id and initial_fields are required")
	}
	seen := map[string]bool{}
	for _, field := range in.InitialFields {
		if err := field.validate(); err != nil {
			return err
		}
		var column string
		_ = json.Unmarshal(field["column_id"], &column)
		if seen[column] {
			return fmt.Errorf("duplicate list column_id")
		}
		seen[column] = true
	}
	return nil
}

func (c *Client) CreateListItem(ctx context.Context, in CreateListItemInput) (json.RawMessage, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	var out struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Item  json.RawMessage `json:"item"`
	}
	if err := c.postJSONNonIdempotent(ctx, "slackLists.items.create", in, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, slackWriteError("slackLists.items.create", out.Error)
	}
	var receipt struct {
		ID     string `json:"id"`
		ListID string `json:"list_id"`
	}
	if json.Unmarshal(out.Item, &receipt) != nil || receipt.ID == "" || receipt.ListID != in.ListID {
		return nil, uncertainDeliveryError{err: fmt.Errorf("Slack did not return the created item identity")}
	}
	return out.Item, nil
}
