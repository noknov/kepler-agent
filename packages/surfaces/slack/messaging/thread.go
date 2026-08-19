package messaging

import (
	"context"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
	slackfiles "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/files"
)

// ThreadLoader fetches Slack thread history, preferring the caller's OAuth token
// for file access and falling back to the workspace bot token.
type ThreadLoader struct {
	Bot       *slack.Client
	BotUserID string
	UserToken func(ctx context.Context, userID string) (string, error)
}

func (l ThreadLoader) Load(ctx context.Context, req slackconversation.Request) []model.Message {
	client := l.ClientFor(ctx, req.UserID)
	if client == nil {
		return nil
	}
	return slackfiles.ThreadHistory(ctx, client, req.Channel, req.ThreadTS, req.MessageTS, slackfiles.MaxThreadHistoryMessages)
}

// ClientFor resolves the Slack API client for thread reads.
func (l ThreadLoader) ClientFor(ctx context.Context, userID string) *slack.Client {
	return l.client(ctx, userID)
}

func (l ThreadLoader) client(ctx context.Context, userID string) *slack.Client {
	if l.UserToken != nil {
		if token, err := l.UserToken(ctx, userID); err == nil && strings.TrimSpace(token) != "" {
			return slack.NewClient(token, l.BotUserID)
		}
	}
	return l.Bot
}
