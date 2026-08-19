package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
	slackfiles "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/files"
)

// ThreadLoader fetches Slack thread history with the requesting user's OAuth token.
// There is no bot-token fallback: CLI and hosted worker both read as the user.
type ThreadLoader struct {
	BotUserID string
	UserToken func(ctx context.Context, userID string) (string, error)
}

func (l ThreadLoader) Load(ctx context.Context, req slackconversation.Request) []model.Message {
	client := l.client(ctx, req.UserID)
	if client == nil {
		return nil
	}
	return slackfiles.ThreadHistory(ctx, client, req.Channel, req.ThreadTS, req.MessageTS, slackfiles.MaxThreadHistoryMessages)
}

func (l ThreadLoader) client(ctx context.Context, userID string) *slack.Client {
	if l.UserToken == nil {
		return nil
	}
	token, err := l.UserToken(ctx, userID)
	if err != nil || strings.TrimSpace(token) == "" {
		log.Printf("slack thread load skipped for user %s: %v", userID, err)
		return nil
	}
	return slack.NewClient(token, l.BotUserID)
}
