package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	slackfiles "github.com/noknov/kepler-agent/packages/surfaces/slack/files"
)

// ThreadLoader preloads thread history for the active bot conversation.
// Passive ingress reads the channel with the bot token; user OAuth is for
// agent-initiated cross-conversation reads via slack-user_read_thread.
type ThreadLoader struct {
	Bot *slack.Client
}

func (l ThreadLoader) Load(ctx context.Context, req slackconversation.Request) ([]model.Message, error) {
	if l.Bot == nil {
		return nil, fmt.Errorf("Slack bot client is unavailable")
	}
	// A root Slack message always starts a new agent conversation. In
	// particular, Slack's Messages tab delivers root messages in the app DM
	// with ThreadTS equal to MessageTS. Reading conversations.history for that
	// shape would inject earlier, unrelated root conversations into this turn.
	if sameTimestamp(req.ThreadTS, req.MessageTS) {
		return nil, nil
	}
	return slackfiles.ThreadHistory(ctx, l.Bot, req.Channel, req.ThreadTS, req.MessageTS, slackfiles.MaxThreadHistoryMessages)
}

func sameTimestamp(left, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(left) == strings.TrimSpace(right)
}
