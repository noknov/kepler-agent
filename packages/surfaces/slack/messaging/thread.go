package messaging

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
	slackfiles "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/files"
)

// ThreadLoader fetches Slack thread history for the active bot conversation.
// It always uses the bot token: the bot is in the channel and needs files:read
// to download attachments. User OAuth is reserved for cross-conversation reads.
type ThreadLoader struct {
	Bot *slack.Client
}

func (l ThreadLoader) Load(ctx context.Context, req slackconversation.Request) []model.Message {
	if l.Bot == nil {
		return nil
	}
	return slackfiles.ThreadHistory(ctx, l.Bot, req.Channel, req.ThreadTS, req.MessageTS, slackfiles.MaxThreadHistoryMessages)
}
