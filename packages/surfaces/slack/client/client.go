package slack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type Client struct {
	token      string
	botUserID  string
	teamID     string
	httpClient *http.Client
}

const (
	// MaxMessageTextRunes is Slack's documented safe size for chat.postMessage
	// text. Keeping every request within it also keeps UTF-8 payloads below the
	// platform's byte-oriented rate-limit guidance.
	MaxMessageTextRunes = 4000
	// MaxSectionTextRunes is the Block Kit section text limit. Callers that
	// render message text into a section must use this smaller bound.
	MaxSectionTextRunes = 3000
)

// MessageBlockBuilder renders Slack blocks for one independently delivered
// text part. A nil builder sends plain text.
type MessageBlockBuilder func(string) []map[string]any

func NewClient(token, botUserID string) *Client {
	return &Client{
		token:      token,
		botUserID:  botUserID,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

// NewTestClient builds a client with a custom HTTP transport for tests.
func NewTestClient(transport http.RoundTripper) *Client {
	return &Client{httpClient: &http.Client{Transport: transport}}
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
		return "", slackAPIError{Method: "auth.test", Code: out.Error}
	}
	if out.TeamID != "" {
		c.teamID = out.TeamID
	}
	return out.UserID, nil
}

func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string) (string, error) {
	return c.PostChunkedMessage(ctx, channel, threadTS, text, "", MaxMessageTextRunes, nil)
}

func (c *Client) PostMessageBlocks(ctx context.Context, channel, threadTS, text string, blocks []map[string]any) (string, error) {
	return c.postMessage(ctx, channel, threadTS, text, blocks, "")
}

func (c *Client) PostMarkdownMessage(ctx context.Context, channel, threadTS, markdown string) (string, error) {
	return c.postMarkdown(ctx, channel, threadTS, markdown, "")
}

func (c *Client) PostMarkdownMessageWithID(ctx context.Context, channel, threadTS, markdown, deliveryID string) (string, error) {
	return c.postMarkdown(ctx, channel, threadTS, markdown, deliveryID)
}

func (c *Client) postMarkdown(ctx context.Context, channel, threadTS, markdown, deliveryID string) (string, error) {
	return c.postTextParts(ctx, channel, threadTS, markdown, deliveryID, MaxMessageTextRunes, func(ctx context.Context, threadTS, part, clientMessageID string) (string, error) {
		blocks := []map[string]any{{"type": "markdown", "text": part}}
		ts, err := c.postMessage(ctx, channel, threadTS, part, blocks, clientMessageID)
		if err == nil || !isSlackErrorCode(err, "invalid_blocks") {
			return ts, err
		}
		// The markdown block is only available to Slack apps with the platform AI
		// feature enabled. Plain text is a valid fallback for other app installs.
		return c.postMessage(ctx, channel, threadTS, part, nil, clientMessageID)
	})
}

// PostChunkedMessage posts text in Slack-safe parts. When a root message needs
// multiple parts, the first remains the root and each continuation is a reply
// to it; callers therefore retain a single conversational unit. deliveryID is
// deterministically expanded per part so retrying a partially delivered event
// cannot create duplicate continuations.
func (c *Client) PostChunkedMessage(ctx context.Context, channel, threadTS, text, deliveryID string, maxRunes int, blocks MessageBlockBuilder) (string, error) {
	return c.postTextParts(ctx, channel, threadTS, text, deliveryID, maxRunes, func(ctx context.Context, threadTS, part, clientMessageID string) (string, error) {
		partBlocks := []map[string]any(nil)
		if blocks != nil {
			partBlocks = blocks(part)
		}
		return c.postMessage(ctx, channel, threadTS, part, partBlocks, clientMessageID)
	})
}

type messagePartSender func(context.Context, string, string, string) (string, error)

func (c *Client) postTextParts(ctx context.Context, channel, threadTS, text, deliveryID string, maxRunes int, send messagePartSender) (string, error) {
	parts := splitSlackMarkdown(text, maxRunes)
	continuationThreadTS := threadTS
	firstTS := ""
	for index, part := range parts {
		clientMessageID := slackClientMessageID(partDeliveryID(deliveryID, index, len(parts)))
		ts, err := send(ctx, continuationThreadTS, part, clientMessageID)
		if err != nil {
			return firstTS, err
		}
		if firstTS == "" {
			firstTS = ts
			if continuationThreadTS == "" {
				continuationThreadTS = ts
			}
		}
	}
	return firstTS, nil
}

func partDeliveryID(deliveryID string, index, total int) string {
	if deliveryID == "" {
		return ""
	}
	if total == 1 {
		return deliveryID
	}
	return fmt.Sprintf("%s:%d", deliveryID, index)
}

func (c *Client) UpdateMarkdownMessage(ctx context.Context, channel, messageTS, markdown string) error {
	blocks := []map[string]any{{"type": "markdown", "text": markdown}}
	if err := c.updateMessage(ctx, channel, messageTS, markdown, blocks); err == nil || !strings.Contains(err.Error(), "invalid_blocks") {
		return err
	}
	return c.updateMessage(ctx, channel, messageTS, markdown, nil)
}

func (c *Client) updateMessage(ctx context.Context, channel, messageTS, text string, blocks []map[string]any) error {
	payload := map[string]any{"channel": channel, "ts": messageTS, "text": text, "unfurl_links": false}
	if len(blocks) > 0 {
		payload["blocks"] = blocks
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.update", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: "chat.update", Code: out.Error}
	}
	return nil
}

func (c *Client) postMessage(ctx context.Context, channel, threadTS, text string, blocks []map[string]any, clientMessageID string) (string, error) {
	payload := map[string]any{
		"channel":      channel,
		"text":         text,
		"unfurl_links": false,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	if len(blocks) > 0 {
		payload["blocks"] = blocks
	}
	if clientMessageID != "" {
		payload["client_msg_id"] = clientMessageID
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
		return "", slackAPIError{Method: "chat.postMessage", Code: out.Error}
	}
	return out.TS, nil
}

// slackAPIError preserves Slack's machine-readable error code so an ingress
// worker can distinguish invalid requests from transient delivery failures.
type slackAPIError struct {
	Method string
	Code   string
}

func (e slackAPIError) Error() string {
	return fmt.Sprintf("slack %s failed: %s", e.Method, e.Code)
}

// Permanent reports errors that cannot succeed by retrying an unchanged API
// request. This includes Slack's message and block size validation failures.
func (e slackAPIError) Permanent() bool {
	switch e.Code {
	case "msg_too_long", "msg_blocks_too_long", "invalid_blocks", "invalid_blocks_format", "invalid_arguments", "no_text":
		return true
	default:
		return false
	}
}

func isSlackErrorCode(err error, code string) bool {
	var apiErr slackAPIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func splitSlackMarkdown(text string, maxRunes int) []string {
	if maxRunes <= 0 || maxRunes > MaxMessageTextRunes {
		maxRunes = MaxMessageTextRunes
	}
	// Reserve enough room to close and reopen a fenced code block if a split
	// falls inside one. This makes every Slack message valid Markdown by itself.
	contentLimit := maxRunes - 8
	if contentLimit < 1 {
		contentLimit = maxRunes
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}

	var parts []string
	inFence := false
	for start := 0; start < len(runes); {
		end := start + contentLimit
		if end >= len(runes) {
			end = len(runes)
		} else {
			end = slackSplitBoundary(runes, start, end)
		}
		body := string(runes[start:end])
		prefix := ""
		if inFence {
			prefix = "```\n"
		}
		inFence = togglesCodeFence(inFence, body)
		suffix := ""
		if inFence {
			suffix = "\n```"
		}
		parts = append(parts, prefix+body+suffix)
		start = end
	}
	return parts
}

func slackSplitBoundary(runes []rune, start, end int) int {
	for index := end; index > start; index-- {
		if runes[index-1] == '\n' {
			return index
		}
	}
	for index := end; index > start; index-- {
		if unicode.IsSpace(runes[index-1]) {
			return index
		}
	}
	return end
}

func togglesCodeFence(inFence bool, text string) bool {
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
	}
	return inFence
}

func slackClientMessageID(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
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
		return slackAPIError{Method: "views.publish", Code: out.Error}
	}
	return nil
}

func (c *Client) OpenView(ctx context.Context, triggerID string, view map[string]any) error {
	payload := map[string]any{
		"trigger_id": triggerID,
		"view":       view,
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "views.open", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: "views.open", Code: out.Error}
	}
	return nil
}

func (c *Client) UpdateView(ctx context.Context, viewID string, view map[string]any) error {
	payload := map[string]any{
		"view_id": viewID,
		"view":    view,
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "views.update", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: "views.update", Code: out.Error}
	}
	return nil
}

func (c *Client) StartStream(ctx context.Context, request slackconversation.StreamStart) (string, error) {
	payload := map[string]any{
		"channel":   request.Channel,
		"thread_ts": request.ThreadTS,
	}
	if request.TaskDisplayMode != "" {
		payload["task_display_mode"] = request.TaskDisplayMode
	}
	if len(request.Chunks) > 0 {
		payload["chunks"] = request.Chunks
	}
	// Slack requires recipient metadata for channel streams; a DM already
	// identifies its recipient, so its request uses only the documented DM
	// arguments.
	if streamingChannelRequiresRecipient(request.Channel) && request.RecipientUserID != "" {
		payload["recipient_user_id"] = request.RecipientUserID
		if c.teamID != "" {
			payload["recipient_team_id"] = c.teamID
		}
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
		return "", slackAPIError{Method: "chat.startStream", Code: out.Error}
	}
	return out.TS, nil
}

func streamingChannelRequiresRecipient(channel string) bool {
	channel = strings.TrimSpace(channel)
	return strings.HasPrefix(channel, "C") || strings.HasPrefix(channel, "G")
}

func (c *Client) AppendStream(ctx context.Context, channel, messageTS string, chunks []map[string]any) error {
	payload := map[string]any{
		"channel": channel,
		"ts":      messageTS,
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
		return slackAPIError{Method: "chat.appendStream", Code: out.Error}
	}
	return nil
}

func (c *Client) StopStream(ctx context.Context, channel, messageTS string) error {
	payload := map[string]any{"channel": channel, "ts": messageTS}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "chat.stopStream", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: "chat.stopStream", Code: out.Error}
	}
	return nil
}

// SetAgentSessionStatus updates the lifecycle state for a thread-based Slack
// agent session. initiatorUserID is used only when Slack creates the session.
func (c *Client) SetAgentSessionStatus(ctx context.Context, channel, threadTS, initiatorUserID, status string) error {
	payload := map[string]any{
		"channel_id": channel,
		"thread_ts":  threadTS,
		"status":     status,
	}
	if initiatorUserID != "" {
		payload["initiator_user_id"] = initiatorUserID
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "agents.sessions.setStatus", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return slackAPIError{Method: "agents.sessions.setStatus", Code: out.Error}
	}
	return nil
}

func (c *Client) UserInfo(ctx context.Context, userID string) (User, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return User{}, fmt.Errorf("slack user id is required")
	}
	values := url.Values{}
	values.Set("user", userID)
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		User  User   `json:"user,omitempty"`
	}
	if err := c.get(ctx, "users.info", values, &out); err != nil {
		return User{}, err
	}
	if !out.OK {
		return User{}, slackAPIError{Method: "users.info", Code: out.Error}
	}
	return out.User, nil
}

// UploadFile uploads data to Slack using the v2 upload API and shares it into
// a channel thread. filename should include an extension (e.g. "screenshot.png").
// Returns the file permalink on success.
func (c *Client) UploadFile(ctx context.Context, channel, threadTS, filename string, data []byte) (string, error) {
	// Step 1: obtain a pre-signed upload URL.
	values := url.Values{}
	values.Set("filename", filename)
	values.Set("length", fmt.Sprintf("%d", len(data)))
	var urlOut struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error,omitempty"`
		UploadURL string `json:"upload_url,omitempty"`
		FileID    string `json:"file_id,omitempty"`
	}
	if err := c.get(ctx, "files.getUploadURLExternal", values, &urlOut); err != nil {
		return "", fmt.Errorf("slack files.getUploadURLExternal: %w", err)
	}
	if !urlOut.OK {
		return "", fmt.Errorf("slack files.getUploadURLExternal failed: %s", urlOut.Error)
	}

	// Step 2: POST the file content to the pre-signed upload URL.
	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlOut.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Content-Type", "application/octet-stream")
	uploadResp, err := c.httpClient.Do(uploadReq)
	if err != nil {
		return "", fmt.Errorf("slack file upload: %w", err)
	}
	uploadBody, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 1<<10))
	uploadResp.Body.Close()
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return "", fmt.Errorf("slack file upload: status %d: %s", uploadResp.StatusCode, strings.TrimSpace(string(uploadBody)))
	}

	// Step 3: complete the upload and share into the channel thread.
	// The files.completeUploadExternal endpoint requires "channel_id" (not "channel").
	completePayload := map[string]any{
		"files":      []map[string]string{{"id": urlOut.FileID, "title": filename}},
		"channel_id": channel,
	}
	if threadTS != "" {
		completePayload["thread_ts"] = threadTS
	}
	var completeOut struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		Files []struct {
			Permalink string `json:"permalink,omitempty"`
		} `json:"files,omitempty"`
	}
	if err := c.postJSON(ctx, "files.completeUploadExternal", completePayload, &completeOut); err != nil {
		return "", fmt.Errorf("slack files.completeUploadExternal: %w", err)
	}
	if !completeOut.OK {
		return "", fmt.Errorf("slack files.completeUploadExternal failed: %s", completeOut.Error)
	}
	if len(completeOut.Files) > 0 {
		return completeOut.Files[0].Permalink, nil
	}
	return "", nil
}

func (c *Client) FileInfo(ctx context.Context, fileID string) (File, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return File{}, fmt.Errorf("slack file id is required")
	}
	values := url.Values{}
	values.Set("file", fileID)
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		File  File   `json:"file,omitempty"`
	}
	if err := c.get(ctx, "files.info", values, &out); err != nil {
		return File{}, err
	}
	if !out.OK {
		return File{}, slackAPIError{Method: "files.info", Code: out.Error}
	}
	return out.File, nil
}

func (c *Client) DownloadFile(ctx context.Context, file File, maxBytes int64) ([]byte, error) {
	if strings.TrimSpace(file.URLPrivateDownload) == "" && strings.TrimSpace(file.URLPrivate) == "" && strings.TrimSpace(file.ID) != "" {
		info, err := c.FileInfo(ctx, file.ID)
		if err != nil {
			return nil, err
		}
		file = mergeFile(file, info)
	}
	fileURL := strings.TrimSpace(file.URLPrivateDownload)
	if fileURL == "" {
		fileURL = strings.TrimSpace(file.URLPrivate)
	}
	if fileURL == "" {
		return nil, fmt.Errorf("slack file %s has no private download URL", file.ID)
	}
	parsedURL, err := url.Parse(fileURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Slack download URL: %w", err)
	}
	if parsedURL.Scheme != "https" || parsedURL.User != nil || !isSlackDownloadHost(parsedURL.Hostname()) {
		return nil, fmt.Errorf("refusing to send Slack credentials to untrusted download host %q", parsedURL.Hostname())
	}
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("slack file download failed: status=%d body=%s", resp.StatusCode, string(data))
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("slack file %s exceeds %d bytes", file.ID, maxBytes)
	}
	return data, nil
}

func isSlackDownloadHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "slack.com" || strings.HasSuffix(host, ".slack.com") || host == "slack-edge.com" || strings.HasSuffix(host, ".slack-edge.com")
}

func mergeFile(primary, fallback File) File {
	if primary.ID == "" {
		primary.ID = fallback.ID
	}
	if primary.Name == "" {
		primary.Name = fallback.Name
	}
	if primary.Title == "" {
		primary.Title = fallback.Title
	}
	if primary.Mimetype == "" {
		primary.Mimetype = fallback.Mimetype
	}
	if primary.Filetype == "" {
		primary.Filetype = fallback.Filetype
	}
	if primary.PrettyType == "" {
		primary.PrettyType = fallback.PrettyType
	}
	if primary.Mode == "" {
		primary.Mode = fallback.Mode
	}
	if primary.Size == 0 {
		primary.Size = fallback.Size
	}
	if primary.URLPrivate == "" {
		primary.URLPrivate = fallback.URLPrivate
	}
	if primary.URLPrivateDownload == "" {
		primary.URLPrivateDownload = fallback.URLPrivateDownload
	}
	if primary.Permalink == "" {
		primary.Permalink = fallback.Permalink
	}
	return primary
}

// IsIMChannel reports whether channel is a Slack direct-message channel (D...).
func IsIMChannel(channel string) bool {
	return strings.HasPrefix(strings.TrimSpace(channel), "D")
}

func (c *Client) History(ctx context.Context, channel, latest string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	values := url.Values{}
	values.Set("channel", channel)
	if latest := strings.TrimSpace(latest); latest != "" {
		values.Set("latest", latest)
		values.Set("inclusive", "false")
	}
	pageLimit := limit
	if pageLimit > 200 {
		pageLimit = 200
	}
	values.Set("limit", fmt.Sprintf("%d", pageLimit))
	var messages []Message
	for len(messages) < limit {
		var out struct {
			OK               bool      `json:"ok"`
			Error            string    `json:"error,omitempty"`
			Messages         []Message `json:"messages,omitempty"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor,omitempty"`
			} `json:"response_metadata,omitempty"`
		}
		if err := c.get(ctx, "conversations.history", values, &out); err != nil {
			return nil, err
		}
		if !out.OK {
			return nil, slackAPIError{Method: "conversations.history", Code: out.Error}
		}
		messages = append(messages, out.Messages...)
		if len(messages) >= limit {
			return messages[:limit], nil
		}
		cursor := strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if cursor == "" {
			return messages, nil
		}
		values.Set("cursor", cursor)
	}
	return messages, nil
}

func (c *Client) Replies(ctx context.Context, channel, threadTS string, limit int) ([]Message, error) {
	all := limit <= 0
	values := url.Values{}
	values.Set("channel", channel)
	values.Set("ts", threadTS)
	pageLimit := limit
	if all || pageLimit > 200 {
		pageLimit = 200
	}
	if pageLimit <= 0 {
		pageLimit = 200
	}
	values.Set("limit", fmt.Sprintf("%d", pageLimit))
	var replies []Message
	for {
		var out struct {
			OK               bool      `json:"ok"`
			Error            string    `json:"error,omitempty"`
			Messages         []Message `json:"messages,omitempty"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor,omitempty"`
			} `json:"response_metadata,omitempty"`
		}
		if err := c.get(ctx, "conversations.replies", values, &out); err != nil {
			return nil, err
		}
		if !out.OK {
			return nil, slackAPIError{Method: "conversations.replies", Code: out.Error}
		}
		replies = append(replies, out.Messages...)
		if !all && len(replies) >= limit {
			return replies[:limit], nil
		}
		cursor := strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if cursor == "" {
			return replies, nil
		}
		values.Set("cursor", cursor)
	}
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
		if file.ID != "" {
			line += " id=" + file.ID
		}
		if file.Permalink != "" {
			line += " " + file.Permalink
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Note: supported image files are sent to the model as images, and PDFs may include extracted text. Markdown, JSON, and plain-text files are listed by metadata only; use slack-file_search with the file id to retrieve relevant sections, or slack-json_analyze for JSON statistics.")
	return strings.Join(lines, "\n")
}

var mentionPattern = regexp.MustCompile(`<@([A-Z0-9]+)>`)

func NormalizeMentions(text, botUserID string) string {
	return mentionPattern.ReplaceAllStringFunc(text, func(match string) string {
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
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		attemptReq, err := cloneRequestForAttempt(req)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(attemptReq)
		if err != nil {
			lastErr = err
			if !shouldRetrySlack(req.Context(), attempt, 0, err) {
				return err
			}
			if err := sleepSlackRetry(req.Context(), attempt, 0); err != nil {
				return err
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = slackHTTPError{StatusCode: resp.StatusCode, Body: string(data)}
			if !shouldRetrySlack(req.Context(), attempt, resp.StatusCode, nil) {
				return lastErr
			}
			if err := sleepSlackRetry(req.Context(), attempt, retryAfter(resp)); err != nil {
				return err
			}
			continue
		}
		return json.NewDecoder(bytes.NewReader(data)).Decode(out)
	}
	return lastErr
}

type slackHTTPError struct {
	StatusCode int
	Body       string
}

func (e slackHTTPError) Error() string {
	return fmt.Sprintf("slack http status %d: %s", e.StatusCode, e.Body)
}

func cloneRequestForAttempt(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.GetBody == nil {
		return clone, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

func shouldRetrySlack(ctx context.Context, attempt, status int, err error) bool {
	if attempt >= 2 || ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func sleepSlackRetry(ctx context.Context, attempt int, retryAfter time.Duration) error {
	delay := retryAfter
	if delay <= 0 {
		delay = time.Duration(attempt+1) * 250 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfter(resp *http.Response) time.Duration {
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		return time.Until(when)
	}
	return 0
}
