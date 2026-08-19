package messaging

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
	slackfiles "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/files"
)

// ThreadLoader preloads thread history for the active bot conversation.
// Passive ingress reads the channel with the bot token; user OAuth is for
// agent-initiated cross-conversation reads via slack-user_read_thread.
type ThreadLoader struct {
	Bot *slack.Client
}

func (l ThreadLoader) Load(ctx context.Context, req slackconversation.Request) []model.Message {
	if l.Bot == nil {
		return nil
	}
	return slackfiles.ThreadHistory(ctx, l.Bot, req.Channel, req.ThreadTS, req.MessageTS, slackfiles.MaxThreadHistoryMessages)
}
