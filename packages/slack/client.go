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
		"channel":           channel,
		"thread_ts":         threadTS,
		"task_display_mode": "dense",
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
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
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

func (c *Client) ThreadContext(ctx context.Context, channel, threadTS string, limit int) string {
	replies, err := c.Replies(ctx, channel, threadTS, limit)
	if err != nil || len(replies) == 0 {
		return ""
	}
	return formatThreadContext(replies, c.botUserID)
}

func formatThreadContext(replies []Message, botUserID string) string {
	lines := make([]string, 0, len(replies))
	for _, msg := range replies {
		if msg.User == botUserID || msg.BotID != "" {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		filesText := FormatFiles(msg.Files)
		if text == "" && filesText == "" {
			continue
		}
		role := msg.User
		content := NormalizeMentions(text, botUserID)
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
		if file.ID != "" {
			line += " id=" + file.ID
		}
		if file.Permalink != "" {
			line += " " + file.Permalink
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Note: on the current turn, supported image files are sent to the model as images, and PDF, Markdown, JSON, and plain-text files as extracted text. For JSON statistics use slack-json_analyze with the file id; for large text/PDF files use slack-file_search with the file id to retrieve relevant sections.")
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
