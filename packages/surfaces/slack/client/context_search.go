package slack

import (
	"context"
	"fmt"
	"strings"
)

const MaxContextSearchResults = 20

// ContextSearchInput requests one permission-scoped RTS page. It deliberately
// has no token/action_token field: credentials belong to the configured client.
type ContextSearchInput struct {
	Query            string   `json:"query"`
	ChannelTypes     []string `json:"channel_types,omitempty"`
	ContextChannelID string   `json:"context_channel_id,omitempty"`
	Cursor           string   `json:"cursor,omitempty"`
	Limit            int      `json:"limit,omitempty"`
}

func (in ContextSearchInput) Validate() error {
	if strings.TrimSpace(in.Query) == "" {
		return fmt.Errorf("Slack search query is required")
	}
	if in.Limit < 0 || in.Limit > MaxContextSearchResults {
		return fmt.Errorf("Slack search limit must be between 1 and %d, or omitted", MaxContextSearchResults)
	}
	for _, kind := range in.ChannelTypes {
		switch kind {
		case "public_channel", "private_channel", "im", "mpim":
		default:
			return fmt.Errorf("unsupported Slack search channel type")
		}
	}
	return nil
}

type ContextSearchMessage struct {
	TeamID          string `json:"team_id,omitempty"`
	ChannelID       string `json:"channel_id"`
	MessageTS       string `json:"message_ts"`
	ThreadTS        string `json:"thread_ts,omitempty"`
	AuthorUserID    string `json:"author_user_id,omitempty"`
	IsAuthorBot     bool   `json:"is_author_bot,omitempty"`
	Content         string `json:"content"`
	Permalink       string `json:"permalink"`
	ContextMessages struct {
		Before []ContextSearchNeighbor `json:"before,omitempty"`
		After  []ContextSearchNeighbor `json:"after,omitempty"`
	} `json:"context_messages,omitempty"`
}

type ContextSearchNeighbor struct {
	UserID string `json:"user_id,omitempty"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

type ContextSearchResult struct {
	Messages   []ContextSearchMessage `json:"messages"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

// SearchContext retrieves a single relevance-sorted message page. No automatic
// cursor drain, history enrichment, query rewrite or legacy endpoint fallback.
func (c *Client) SearchContext(ctx context.Context, in ContextSearchInput) (ContextSearchResult, error) {
	if err := in.Validate(); err != nil {
		return ContextSearchResult{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = MaxContextSearchResults
	}
	payload := map[string]any{"query": strings.TrimSpace(in.Query), "content_types": []string{"messages"}, "include_context_messages": true, "sort": "score", "limit": limit}
	if len(in.ChannelTypes) > 0 {
		payload["channel_types"] = in.ChannelTypes
	}
	if in.ContextChannelID != "" {
		payload["context_channel_id"] = in.ContextChannelID
	}
	if in.Cursor != "" {
		payload["cursor"] = in.Cursor
	}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
		Results struct {
			Messages []ContextSearchMessage `json:"messages"`
		} `json:"results"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := c.postJSON(ctx, "assistant.search.context", payload, &out); err != nil {
		return ContextSearchResult{}, err
	}
	if !out.OK {
		return ContextSearchResult{}, slackAPIError{Method: "assistant.search.context", Code: out.Error}
	}
	if len(out.Results.Messages) > limit {
		return ContextSearchResult{}, fmt.Errorf("Slack search response exceeds requested result limit")
	}
	for _, message := range out.Results.Messages {
		if message.ChannelID == "" || message.MessageTS == "" || message.Permalink == "" {
			return ContextSearchResult{}, fmt.Errorf("Slack search response lacks message source identity")
		}
	}
	next, err := checkedPaginationCursor("assistant.search.context", out.Metadata.NextCursor, map[string]bool{strings.TrimSpace(in.Cursor): true})
	if err != nil {
		return ContextSearchResult{}, err
	}
	return ContextSearchResult{Messages: out.Results.Messages, NextCursor: next}, nil
}
