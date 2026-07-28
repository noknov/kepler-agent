package slackbot

import (
	"github.com/noknov/slack-copilot-agent/packages/appsupport"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
	"github.com/noknov/slack-copilot-agent/packages/slack"
)

func newAutoTTSFunc(cfg config.Config, slackClient *slack.Client) conversation.AutoTTSFunc {
	return appsupport.NewAutoTTSFunc(cfg, slackClient)
}

func newTTSSummarizer(cfg config.Config, runtime appruntime.AgentRuntime) *conversation.TTSSummarizer {
	return appsupport.NewTTSSummarizer(cfg, runtime)
}
