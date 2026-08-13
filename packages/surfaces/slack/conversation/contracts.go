// Package slackconversation defines the thin transport contract between Slack
// ingress/presentation and the agent profile. It deliberately contains no
// agent loop, persistence, prompt, or product policy.
package slackconversation

import (
	"context"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

type Request struct {
	EventID   string
	UserID    string
	Channel   string
	ThreadTS  string
	MessageTS string
	Text      string
	Locale    string
	Content   []model.Content
	ClaimID   string `json:"-"`
}

func (r Request) Message() model.Message {
	content := make([]model.Content, 0, len(r.Content)+1)
	if r.Text != "" {
		content = append(content, model.Content{Type: model.ContentText, Text: r.Text})
	}
	content = append(content, r.Content...)
	return model.Message{Role: model.RoleUser, Content: content}
}

type Conversation interface {
	HandleMention(context.Context, Request) (bool, error)
	HandleReply(context.Context, Request) (bool, error)
}

type ControlledConversation interface {
	Conversation
	StartControlSubscriber(context.Context)
}

type Messenger interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
	PostMarkdownMessage(ctx context.Context, channel, threadTS, markdown string) (string, error)
}

type IdempotentMarkdownMessenger interface {
	PostMarkdownMessageWithID(ctx context.Context, channel, threadTS, markdown, deliveryID string) (string, error)
}

type ThreadStatusMessenger interface {
	SetThreadStatus(ctx context.Context, channel, threadTS, status string, loadingMessages []string) error
}

type ThreadHistoryMessenger interface {
	ThreadHistory(ctx context.Context, channel, threadTS, beforeTS string, limit int) []model.Message
}

func IsChineseLocale(locale string) bool {
	locale = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	return locale == "zh" || strings.HasPrefix(locale, "zh-")
}
