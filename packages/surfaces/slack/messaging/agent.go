package messaging

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	slackfiles "github.com/noknov/slack-copilot-agent/packages/surfaces/slack/files"
)

// AgentMessenger adapts the Slack API client for agent ingress, including
// multimodal thread history that reuses the shared attachment pipeline.
type AgentMessenger struct {
	*slack.Client
}

func (m AgentMessenger) ThreadHistory(ctx context.Context, channel, threadTS, beforeTS string, limit int) []model.Message {
	if m.Client == nil {
		return nil
	}
	return slackfiles.ThreadHistory(ctx, m.Client, channel, threadTS, beforeTS, limit)
}
