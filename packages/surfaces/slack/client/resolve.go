package slack

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	slackArchivePath = regexp.MustCompile(`/archives/([CGD][A-Z0-9]+)/p([0-9]+)`)
	slackUserID      = regexp.MustCompile(`^U[A-Z0-9]+$`)
)

// ResolveReadTarget maps flexible user input into a channel/thread read target.
func (c *Client) ResolveReadTarget(ctx context.Context, in ReadTargetInput) (ReadTarget, error) {
	channel := NormalizeChannelRef(in.Channel)
	threadTS := trim(in.ThreadTS)

	if link := trim(in.Link); link != "" {
		parsedChannel, parsedThread, err := ParseConversationLink(link)
		if err != nil {
			return ReadTarget{}, err
		}
		channel = parsedChannel
		if threadTS == "" {
			threadTS = parsedThread
		}
	}

	if userQuery := trim(in.User); userQuery != "" {
		userID, err := c.resolveUserQuery(ctx, userQuery)
		if err != nil {
			return ReadTarget{}, err
		}
		channel, err = c.OpenIM(ctx, userID)
		if err != nil {
			return ReadTarget{}, err
		}
	}

	if channel != "" && slackUserID.MatchString(channel) {
		opened, err := c.OpenIM(ctx, channel)
		if err != nil {
			return ReadTarget{}, err
		}
		channel = opened
	}

	if channel == "" {
		channel = trim(in.ScopeChannel)
	}
	if channel == "" {
		return ReadTarget{}, fmt.Errorf("channel, user, or link is required")
	}

	latestTS := ""
	if !in.RequiresUserConnection() {
		if threadTS == "" {
			threadTS = trim(in.ScopeThreadTS)
		}
		latestTS = trim(in.ScopeMessageTS)
	}

	return ReadTarget{Channel: channel, ThreadTS: threadTS, LatestTS: latestTS}, nil
}

func (c *Client) OpenIM(ctx context.Context, userID string) (string, error) {
	userID = NormalizeUserID(userID)
	if userID == "" {
		return "", fmt.Errorf("slack user id is required")
	}
	if !slackUserID.MatchString(userID) {
		return "", fmt.Errorf("invalid slack user id %q", userID)
	}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := c.postJSON(ctx, "conversations.open", map[string]any{"users": userID}, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack conversations.open failed: %s", out.Error)
	}
	channel := strings.TrimSpace(out.Channel.ID)
	if channel == "" {
		return "", fmt.Errorf("slack conversations.open returned empty channel")
	}
	return channel, nil
}

// FindUsersByQuery searches workspace members by display name, real name, or handle.
func (c *Client) FindUsersByQuery(ctx context.Context, query string, limit int) ([]User, error) {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil, fmt.Errorf("user query is required")
	}
	if limit <= 0 {
		limit = 5
	}
	cursor := ""
	var matches []User
	for len(matches) < limit {
		values := url.Values{}
		values.Set("limit", "200")
		if cursor != "" {
			values.Set("cursor", cursor)
		}
		var out struct {
			OK               bool   `json:"ok"`
			Error            string `json:"error,omitempty"`
			Members          []User `json:"members"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.get(ctx, "users.list", values, &out); err != nil {
			return nil, err
		}
		if !out.OK {
			return nil, fmt.Errorf("slack users.list failed: %s", out.Error)
		}
		for _, member := range out.Members {
			if member.ID == "" || member.Deleted() {
				continue
			}
			if userMatchesQuery(member, query) {
				matches = append(matches, member)
				if len(matches) >= limit {
					return matches, nil
				}
			}
		}
		cursor = strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if cursor == "" {
			return matches, nil
		}
	}
	return matches, nil
}

func (u User) Deleted() bool {
	return u.DeletedFlag
}

func userMatchesQuery(user User, query string) bool {
	fields := []string{
		user.ID,
		user.Name,
		user.RealName,
		user.Profile.DisplayName,
		user.Profile.RealName,
	}
	tokens := strings.Fields(strings.ToLower(query))
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		found := false
		for _, candidate := range fields {
			if strings.Contains(strings.ToLower(strings.TrimSpace(candidate)), token) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (c *Client) resolveUserQuery(ctx context.Context, query string) (string, error) {
	query = NormalizeUserID(query)
	if slackUserID.MatchString(query) {
		return query, nil
	}
	matches, err := c.FindUsersByQuery(ctx, query, 6)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no Slack user matched %q", query)
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, formatUserLabel(match))
		}
		return "", fmt.Errorf("multiple Slack users matched %q: %s; pass a user ID instead", query, strings.Join(names, ", "))
	}
	return matches[0].ID, nil
}

func formatUserLabel(user User) string {
	label := strings.TrimSpace(user.Profile.DisplayName)
	if label == "" {
		label = strings.TrimSpace(user.RealName)
	}
	if label == "" {
		label = strings.TrimSpace(user.Name)
	}
	if label == "" {
		return user.ID
	}
	return label + " (" + user.ID + ")"
}

// NormalizeUserID strips Slack mention wrappers from a user reference.
func NormalizeUserID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<@")
	value = strings.TrimSuffix(value, ">")
	value = strings.TrimPrefix(value, "@")
	if bar := strings.Index(value, "|"); bar >= 0 {
		value = value[:bar]
	}
	return strings.TrimSpace(value)
}

// NormalizeChannelRef strips Slack channel mention wrappers.
func NormalizeChannelRef(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<#") && strings.HasSuffix(value, ">") {
		value = strings.TrimPrefix(strings.TrimSuffix(value, ">"), "<#")
		if bar := strings.Index(value, "|"); bar >= 0 {
			value = value[:bar]
		}
	}
	return strings.TrimSpace(value)
}

// ParseConversationLink extracts channel and optional thread timestamp from a Slack URL.
func ParseConversationLink(raw string) (channel, threadTS string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("slack link is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse slack link: %w", err)
	}
	if thread := strings.TrimSpace(parsed.Query().Get("thread_ts")); thread != "" {
		threadTS = thread
	}
	if cid := strings.TrimSpace(parsed.Query().Get("cid")); cid != "" {
		return cid, threadTS, nil
	}
	if match := slackArchivePath.FindStringSubmatch(parsed.Path); len(match) == 3 {
		channel = match[1]
		if threadTS == "" {
			threadTS = permalinkTSToMessageTS(match[2])
		}
		return channel, threadTS, nil
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "C") || strings.HasPrefix(part, "D") || strings.HasPrefix(part, "G") {
			return part, threadTS, nil
		}
	}
	return "", "", fmt.Errorf("could not parse slack link %q", raw)
}

func permalinkTSToMessageTS(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "p")
	if len(value) <= 6 {
		return value
	}
	return value[:len(value)-6] + "." + value[len(value)-6:]
}
