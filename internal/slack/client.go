package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	token      string
	botUserID  string
	teamID     string
	httpClient *http.Client
}

func NewClient(token, botUserID string) *Client {
	return &Client{
		token:      token,
		botUserID:  botUserID,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) BotUserID() string {
	return c.botUserID
}

func (c *Client) SetBotUserID(botUserID string) {
	c.botUserID = botUserID
}

func (c *Client) AuthTest(ctx context.Context) (string, error) {
	var out struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
		UserID string `json:"user_id,omitempty"`
		TeamID string `json:"team_id,omitempty"`
	}
	if err := c.postJSON(ctx, "auth.test", map[string]any{}, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack auth.test failed: %s", out.Error)
	}
	if out.TeamID != "" {
		c.teamID = out.TeamID
	}
	return out.UserID, nil
}

func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string) (string, error) {
	payload := map[string]any{
		"channel":      channel,
		"text":         text,
		"unfurl_links": false,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		TS    string `json:"ts,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.postMessage", payload, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack chat.postMessage failed: %s", out.Error)
	}
	return out.TS, nil
}

func (c *Client) PublishHome(ctx context.Context, userID string, view map[string]any) error {
	payload := map[string]any{
		"user_id": userID,
		"view":    view,
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "views.publish", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack views.publish failed: %s", out.Error)
	}
	return nil
}

func (c *Client) StartStream(ctx context.Context, channel, threadTS, recipientUserID string) (string, error) {
	payload := map[string]any{
		"channel":   channel,
		"thread_ts": threadTS,
	}
	if recipientUserID != "" {
		payload["recipient_user_id"] = recipientUserID
	}
	if c.teamID != "" {
		payload["recipient_team_id"] = c.teamID
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		TS    string `json:"ts,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.startStream", payload, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack chat.startStream failed: %s", out.Error)
	}
	return out.TS, nil
}

func (c *Client) AppendStream(ctx context.Context, channel, ts string, chunks []map[string]any) error {
	payload := map[string]any{
		"channel": channel,
		"ts":      ts,
		"chunks":  chunks,
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.appendStream", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack chat.appendStream failed: %s", out.Error)
	}
	return nil
}

func (c *Client) StopStream(ctx context.Context, channel, ts string) error {
	payload := map[string]any{"channel": channel, "ts": ts}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.stopStream", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack chat.stopStream failed: %s", out.Error)
	}
	return nil
}

func (c *Client) DeleteMessage(ctx context.Context, channel, ts string) error {
	payload := map[string]any{"channel": channel, "ts": ts}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.delete", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack chat.delete failed: %s", out.Error)
	}
	return nil
}

func (c *Client) Replies(ctx context.Context, channel, threadTS string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 20
	}
	values := url.Values{}
	values.Set("channel", channel)
	values.Set("ts", threadTS)
	values.Set("limit", fmt.Sprintf("%d", limit))
	var out struct {
		OK       bool      `json:"ok"`
		Error    string    `json:"error,omitempty"`
		Messages []Message `json:"messages,omitempty"`
	}
	if err := c.get(ctx, "conversations.replies", values, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("slack conversations.replies failed: %s", out.Error)
	}
	return out.Messages, nil
}

func (c *Client) ThreadContext(ctx context.Context, channel, threadTS string, limit int) string {
	replies, err := c.Replies(ctx, channel, threadTS, limit)
	if err != nil || len(replies) == 0 {
		return ""
	}
	lines := make([]string, 0, len(replies))
	for _, msg := range replies {
		text := strings.TrimSpace(msg.Text)
		filesText := FormatFiles(msg.Files)
		if text == "" && filesText == "" {
			continue
		}
		role := msg.User
		if msg.User == c.botUserID || msg.BotID != "" {
			role = "bot"
		}
		content := NormalizeMentions(text, c.botUserID)
		if filesText != "" {
			if content != "" {
				content += "\n"
			}
			content += filesText
		}
		lines = append(lines, role+": "+content)
	}
	return strings.Join(lines, "\n")
}

func FormatFiles(files []File) string {
	if len(files) == 0 {
		return ""
	}
	lines := make([]string, 0, len(files)+1)
	lines = append(lines, "Uploaded Slack files:")
	for _, file := range files {
		name := strings.TrimSpace(file.Title)
		if name == "" {
			name = strings.TrimSpace(file.Name)
		}
		if name == "" {
			name = strings.TrimSpace(file.ID)
		}
		kind := strings.TrimSpace(file.Mimetype)
		if kind == "" {
			kind = strings.TrimSpace(file.PrettyType)
		}
		if kind == "" {
			kind = strings.TrimSpace(file.Filetype)
		}
		if kind == "" {
			kind = "unknown type"
		}
		line := "- " + name + " (" + kind + ")"
		if file.Permalink != "" {
			line += " " + file.Permalink
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Note: this bot currently receives file metadata only, not image pixels. Ask the user for the visible details if the task requires reading the image.")
	return strings.Join(lines, "\n")
}

func NormalizeMentions(text, botUserID string) string {
	re := regexp.MustCompile(`<@([A-Z0-9]+)>`)
	return re.ReplaceAllStringFunc(text, func(match string) string {
		id := strings.TrimSuffix(strings.TrimPrefix(match, "<@"), ">")
		if id == botUserID {
			return "@bot"
		}
		return "@" + id
	})
}

func (c *Client) postJSON(ctx context.Context, method string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/"+method, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return c.do(req, out)
}

func (c *Client) get(ctx context.Context, method string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://slack.com/api/"+method+"?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack http status %d: %s", resp.StatusCode, string(data))
	}
	return json.NewDecoder(bytes.NewReader(data)).Decode(out)
}
