// Package messaging holds Slack-surface send helpers.
//
// Bot and user-connection messaging are exposed only through the Slack worker
// (handler, agent, tools, reminders). They must not be wired into CLI or other
// surfaces. The API client underneath is just chat.postMessage.
package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

// SendBotUserMessage delivers a proactive bot DM to a Slack user.
// Use only from Slack-surface flows such as conversation replies, reminders,
// or operator-triggered announcements inside the Slack app.
func SendBotUserMessage(ctx context.Context, client *slack.Client, userID, text string) (string, error) {
	userID = strings.TrimSpace(userID)
	text = strings.TrimSpace(text)
	if userID == "" {
		return "", fmt.Errorf("user id is required")
	}
	if text == "" {
		return "", fmt.Errorf("text is required")
	}
	if client == nil {
		return "", fmt.Errorf("slack client is not configured")
	}
	return client.PostMessage(ctx, userID, "", text)
}

// BotUserMessenger adapts reminder delivery to proactive bot DMs.
type BotUserMessenger struct {
	Client *slack.Client
}

func (m BotUserMessenger) PostMessage(ctx context.Context, channel, threadTS, text string) (string, error) {
	if threadTS == "" && strings.HasPrefix(strings.TrimSpace(channel), "U") {
		return SendBotUserMessage(ctx, m.Client, channel, text)
	}
	if m.Client == nil {
		return "", fmt.Errorf("slack client is not configured")
	}
	return m.Client.PostMessage(ctx, channel, threadTS, text)
}
