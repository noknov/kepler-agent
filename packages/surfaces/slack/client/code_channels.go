package slack

import (
	"context"
	"fmt"
	"net/url"
	"unicode/utf8"
)

// CodeChannelInfo is Slack's authoritative conversation classification. Names,
// an empty code_channel object and a missing thread timestamp are not evidence.
type CodeChannelInfo struct {
	ID         string `json:"id"`
	IsArchived bool   `json:"is_archived"`
	Properties struct {
		RecordChannel struct {
			RecordType string `json:"record_type"`
		} `json:"record_channel"`
		AgentSession struct {
			Agents []string `json:"encoded_agent_bot_user_ids"`
			Origin struct {
				Channel string `json:"channel_id"`
				TS      string `json:"ts"`
			} `json:"origin_link"`
		} `json:"agent_session"`
	} `json:"properties"`
}

func (i CodeChannelInfo) IsCodeChannel() bool {
	return i.Properties.RecordChannel.RecordType == "agent_channel"
}
func (i CodeChannelInfo) HasAgent(botID string) bool {
	for _, id := range i.Properties.AgentSession.Agents {
		if botID != "" && id == botID {
			return true
		}
	}
	return false
}

func (c *Client) CodeChannelInfo(ctx context.Context, channel string) (CodeChannelInfo, error) {
	var out struct {
		OK      bool            `json:"ok"`
		Error   string          `json:"error"`
		Channel CodeChannelInfo `json:"channel"`
	}
	if err := c.get(ctx, "conversations.info", url.Values{"channel": {channel}}, &out); err != nil {
		return CodeChannelInfo{}, err
	}
	if !out.OK {
		return CodeChannelInfo{}, slackAPIError{Method: "conversations.info", Code: out.Error}
	}
	if out.Channel.ID != channel {
		return CodeChannelInfo{}, fmt.Errorf("Slack conversation response identity mismatch")
	}
	return out.Channel, nil
}

type CodeChannelCreate struct {
	TeamID          string `json:"team_id"`
	SessionID       string `json:"session_id"`
	OriginChannel   string `json:"origin_channel_id"`
	OriginMessageTS string `json:"origin_message_ts"`
	Name            string `json:"name,omitempty"`
	// Deliberately no is_private: always inherit origin privacy.
}

func (c *Client) CreateCodeChannel(ctx context.Context, request CodeChannelCreate) (string, error) {
	if request.TeamID == "" || request.SessionID == "" || request.OriginChannel == "" || request.OriginMessageTS == "" {
		return "", fmt.Errorf("code channel requires workspace, idempotency key and origin")
	}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel_id"`
	}
	if err := c.postJSON(ctx, "agents.conversations.create", request, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", slackAPIError{Method: "agents.conversations.create", Code: out.Error}
	}
	if out.Channel == "" {
		return "", fmt.Errorf("Slack omitted created code channel identity")
	}
	return out.Channel, nil
}

type CodeContextItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Icon  string `json:"icon,omitempty"`
	URL   string `json:"url,omitempty"`
}

func (c *Client) SetCodeContext(ctx context.Context, channel string, items []CodeContextItem) error {
	if channel == "" || len(items) > 5 {
		return fmt.Errorf("code context requires a channel and at most five items")
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Key == "" || item.Label == "" || utf8.RuneCountInString(item.Key) > 64 || utf8.RuneCountInString(item.Label) > 128 || len(item.URL) > 2048 || seen[item.Key] {
			return fmt.Errorf("invalid or duplicate code context item")
		}
		seen[item.Key] = true
		if item.URL != "" {
			u, err := url.Parse(item.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return fmt.Errorf("code context links require HTTPS")
			}
		}
	}
	if items == nil {
		items = []CodeContextItem{}
	}
	return c.codeChannelWrite(ctx, "agents.conversations.setProperties", map[string]any{"channel_id": channel, "code_channel": map[string]any{"context_bar_items": items}})
}

type CodeView struct {
	Channel     string           `json:"channel_id"`
	Type        string           `json:"type"`
	Key         string           `json:"view_key,omitempty"`
	Name        string           `json:"name,omitempty"`
	Content     string           `json:"content,omitempty"`
	Blocks      []map[string]any `json:"blocks,omitempty"`
	CanvasID    string           `json:"canvas_id,omitempty"`
	AccessLevel string           `json:"access_level,omitempty"`
	BaseBranch  string           `json:"base_branch,omitempty"`
	HeadBranch  string           `json:"head_branch,omitempty"`
	// No model-controlled CSP extensions. HTML remains self-contained.
}

func (c *Client) SetCodeView(ctx context.Context, view CodeView) error {
	if view.Channel == "" || len(view.Content) > 1_000_000 {
		return fmt.Errorf("code view requires a channel and content within one million bytes")
	}
	if view.Type != "diff" && view.Key == "" {
		return fmt.Errorf("code view requires a stable key")
	}
	switch view.Type {
	case "html", "diff":
		if view.Content == "" || len(view.Blocks) > 0 || view.CanvasID != "" {
			return fmt.Errorf("HTML/diff views require only text content")
		}
	case "block_kit":
		if len(view.Blocks) == 0 || view.Content != "" || view.CanvasID != "" {
			return fmt.Errorf("Block Kit views require only blocks")
		}
	case "canvas":
		if view.CanvasID == "" || view.Content != "" || len(view.Blocks) > 0 {
			return fmt.Errorf("canvas views require only a canvas ID")
		}
		// Preserve an agent-owned document: attaching a canvas must not implicitly
		// grant every channel member Slack's default write access.
		view.AccessLevel = "comment"
	default:
		return fmt.Errorf("unsupported code view type")
	}
	return c.codeChannelWrite(ctx, "agents.conversations.setView", view)
}

func (c *Client) codeChannelWrite(ctx context.Context, method string, payload any) error {
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.postJSON(ctx, method, payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: method, Code: out.Error}
	}
	return nil
}
