// Package slackconversation defines the thin transport contract between Slack
// ingress/presentation and the agent profile. It deliberately contains no
// agent loop, persistence, prompt, or product policy.
package slackconversation

import (
	"context"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/model"
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
	StartConnectionCompletedSubscriber(context.Context)
}

type Messenger interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
	PostMarkdownMessage(ctx context.Context, channel, threadTS, markdown string) (string, error)
}

type IdempotentMarkdownMessenger interface {
	PostMarkdownMessageWithID(ctx context.Context, channel, threadTS, markdown, deliveryID string) (string, error)
}

// MarkdownMessageUpdater replaces an existing assistant reply. It lets a
// native stream recover after Slack accepts the message but rejects an append.
type MarkdownMessageUpdater interface {
	UpdateMarkdownMessage(ctx context.Context, channel, messageTS, markdown string) error
}

// NativeStreamMessenger delivers assistant answers through Slack's chat.startStream,
// chat.appendStream, and chat.stopStream APIs.
type NativeStreamMessenger interface {
	StartStream(ctx context.Context, request StreamStart) (string, error)
	AppendStream(ctx context.Context, channel, messageTS string, chunks []map[string]any) error
	StopStream(ctx context.Context, channel, messageTS string) error
}

// StreamStart describes a Slack streaming message. TaskDisplayMode and Chunks
// are used to render structured progress before the assistant starts text.
type StreamStart struct {
	Channel         string
	ThreadTS        string
	RecipientUserID string
	TaskDisplayMode string
	Chunks          []map[string]any
}

// AgentSessionMessenger manages Slack's agent-session lifecycle independently
// of message streaming.
type AgentSessionMessenger interface {
	SetAgentSessionStatus(ctx context.Context, channel, threadTS, initiatorUserID, status string) error
}

func IsChineseLocale(locale string) bool {
	locale = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	return locale == "zh" || strings.HasPrefix(locale, "zh-")
}
