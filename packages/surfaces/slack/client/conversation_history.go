package slack

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) History(ctx context.Context, channel, latest string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	values := url.Values{"channel": {channel}}
	if latest = strings.TrimSpace(latest); latest != "" {
		values.Set("latest", latest)
		values.Set("inclusive", "false")
	}
	return c.conversationMessages(ctx, "conversations.history", values, limit)
}

// Replies retains the explicit limit<=0 all-pages contract. Runtime/tool callers
// supply positive message limits and cancellation; no short/empty-page heuristic
// is used to infer that Slack has finished returning a conversation.
func (c *Client) Replies(ctx context.Context, channel, threadTS string, limit int) ([]Message, error) {
	return c.conversationMessages(ctx, "conversations.replies", url.Values{"channel": {channel}, "ts": {threadTS}}, limit)
}

// conversationMessages owns cursor progression and the requested message budget
// for both conversation APIs, so their failure/termination rules cannot drift.
func (c *Client) conversationMessages(ctx context.Context, method string, values url.Values, limit int) ([]Message, error) {
	seen := make(map[string]bool)
	var messages []Message
	for {
		pageLimit := 200
		if limit > 0 {
			pageLimit = min(pageLimit, limit-len(messages))
		}
		values.Set("limit", strconv.Itoa(pageLimit))
		var page struct {
			OK       bool      `json:"ok"`
			Error    string    `json:"error,omitempty"`
			Messages []Message `json:"messages,omitempty"`
			Metadata struct {
				NextCursor string `json:"next_cursor,omitempty"`
			} `json:"response_metadata,omitempty"`
		}
		if err := c.get(ctx, method, values, &page); err != nil {
			return nil, err
		}
		if !page.OK {
			return nil, slackAPIError{Method: method, Code: page.Error}
		}
		next, err := checkedPaginationCursor(method, page.Metadata.NextCursor, seen)
		if err != nil {
			return nil, err
		}
		messages = append(messages, page.Messages...)
		if limit > 0 && len(messages) >= limit {
			return messages[:limit], nil
		}
		if next == "" {
			return messages, nil
		}
		values.Set("cursor", next)
	}
}
