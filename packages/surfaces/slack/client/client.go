package slack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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
		return "", fmt.Errorf("slack auth.test failed: %s", out.Error)
	}
	if out.TeamID != "" {
		c.teamID = out.TeamID
	}
	return out.UserID, nil
}

func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string) (string, error) {
	return c.postMessage(ctx, channel, threadTS, text, nil, "")
}

func (c *Client) PostMessageBlocks(ctx context.Context, channel, threadTS, text string, blocks []map[string]any) (string, error) {
	return c.postMessage(ctx, channel, threadTS, text, blocks, "")
}

func (c *Client) PostMarkdownMessage(ctx context.Context, channel, threadTS, markdown string) (string, error) {
	blocks := []map[string]any{{"type": "markdown", "text": markdown}}
	ts, err := c.postMessage(ctx, channel, threadTS, markdown, blocks, "")
	if err == nil || !strings.Contains(err.Error(), "invalid_blocks") {
		return ts, err
	}
	// The markdown block is only available to Slack apps with the platform AI
	// feature enabled. Plain text is a valid fallback for other app installs.
	return c.postMessage(ctx, channel, threadTS, markdown, nil, "")
}

func (c *Client) PostMarkdownMessageWithID(ctx context.Context, channel, threadTS, markdown, deliveryID string) (string, error) {
	blocks := []map[string]any{{"type": "markdown", "text": markdown}}
	clientMessageID := slackClientMessageID(deliveryID)
	ts, err := c.postMessage(ctx, channel, threadTS, markdown, blocks, clientMessageID)
	if err == nil || !strings.Contains(err.Error(), "invalid_blocks") {
		return ts, err
	}
	return c.postMessage(ctx, channel, threadTS, markdown, nil, clientMessageID)
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
		return fmt.Errorf("slack chat.update failed: %s", out.Error)
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
		return "", fmt.Errorf("slack chat.postMessage failed: %s", out.Error)
	}
	return out.TS, nil
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
		return fmt.Errorf("slack views.publish failed: %s", out.Error)
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
		return fmt.Errorf("slack views.open failed: %s", out.Error)
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
		return fmt.Errorf("slack views.update failed: %s", out.Error)
	}
	return nil
}

func (c *Client) StartStream(ctx context.Context, channel, threadTS, recipientUserID string) (string, error) {
	payload := map[string]any{
		"channel":   channel,
		"thread_ts": threadTS,
	}
	// Slack requires recipient metadata for channel streams; a DM already
	// identifies its recipient, so its request uses only the documented DM
	// arguments.
	if streamingChannelRequiresRecipient(channel) && recipientUserID != "" {
		payload["recipient_user_id"] = recipientUserID
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
		return "", fmt.Errorf("slack chat.startStream failed: %s", out.Error)
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
		return fmt.Errorf("slack chat.appendStream failed: %s", out.Error)
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
		return fmt.Errorf("slack chat.stopStream failed: %s", out.Error)
	}
	return nil
}

// SetThreadStatus updates the native Slack AI assistant status indicator for a
// thread. Slack automatically clears the status when the app sends a reply;
// passing an empty status clears it explicitly.
func (c *Client) SetThreadStatus(ctx context.Context, channel, threadTS, status string, loadingMessages []string) error {
	payload := map[string]any{
		"channel_id": channel,
		"thread_ts":  threadTS,
		"status":     status,
	}
	if len(loadingMessages) > 0 {
		payload["loading_messages"] = loadingMessages
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.postJSON(ctx, "assistant.threads.setStatus", payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack assistant.threads.setStatus failed: %s", out.Error)
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
		return User{}, fmt.Errorf("slack users.info failed: %s", out.Error)
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
		return File{}, fmt.Errorf("slack files.info failed: %s", out.Error)
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
			return nil, fmt.Errorf("slack conversations.history failed: %s", out.Error)
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
			return nil, fmt.Errorf("slack conversations.replies failed: %s", out.Error)
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
