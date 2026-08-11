// Package slackconversation defines the thin transport contract between Slack
// ingress/presentation and the agent profile. It deliberately contains no
// agent loop, persistence, prompt, or product policy.
package slackconversation

import (
	"context"
	"unicode"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

type Request struct {
	EventID  string
	UserID   string
	Channel  string
	ThreadTS string
	Text     string
	Content  []model.Content
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
	HandleMention(context.Context, Request) bool
	HandleReply(context.Context, Request) bool
}

type ControlledConversation interface {
	Conversation
	StartControlSubscriber(context.Context)
}

type Messenger interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
	StartStream(ctx context.Context, channel, threadTS, recipientUserID string) (string, error)
	AppendStream(ctx context.Context, channel, ts string, chunks []map[string]any) error
	StopStream(ctx context.Context, channel, ts string) error
	DeleteMessage(ctx context.Context, channel, ts string) error
	ThreadContext(ctx context.Context, channel, threadTS string, limit int) string
}

type ThreadStatusMessenger interface {
	SetThreadStatus(ctx context.Context, channel, threadTS, status string, loadingMessages []string) error
}

type TextFormatter func(string) string

func IsCJK(text string) bool {
	for _, value := range text {
		if unicode.Is(unicode.Han, value) || unicode.Is(unicode.Katakana, value) || unicode.Is(unicode.Hangul, value) {
			return true
		}
	}
	return false
}
