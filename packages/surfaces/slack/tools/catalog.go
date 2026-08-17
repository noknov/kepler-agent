package slacktool

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	reminderTools "github.com/noknov/slack-copilot-agent/packages/tools/reminder"
	ttsTools "github.com/noknov/slack-copilot-agent/packages/tools/tts"
)

func AddToCatalog(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, rdb *redisclient.Client) {
	if catalog == nil || slackClient == nil {
		return
	}
	_ = catalog.RegisterVisible(policy, AskUserTool{Slack: slackClient})
	_ = catalog.RegisterVisible(policy, FileSearchTool{Slack: slackClient})
	_ = catalog.RegisterVisible(policy, JSONAnalyzeTool{Slack: slackClient})
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, CreateCanvasTool{Slack: slackClient})
	if reminderStore != nil {
		_ = catalog.RegisterVisible(policy, bindSurface(reminderTools.CreateTool{
			Store: reminderStore,
			OnCreate: func(ctx context.Context) {
				if rdb != nil {
					_ = rdb.Publish(ctx, "reminders:new", "1")
				}
			},
		}, "reminder"))
		_ = catalog.RegisterVisible(policy, bindSurface(reminderTools.ListTool{Store: reminderStore}, "reminder"))
		_ = catalog.RegisterVisible(policy, bindSurface(reminderTools.CancelTool{Store: reminderStore}, "reminder"))
	}
	registerTTS(catalog, policy, cfg, slackClient)
}

func registerTTS(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, slackClient *slack.Client) {
	tts := cfg.Integrations.TTS
	item := bindSurface(ttsTools.SpeakTool{
		Slack:   slackClient,
		APIKey:  tts.APIKey,
		BaseURL: tts.BaseURL,
		Model:   tts.Model,
	}, "tts")
	if tts.APIKey != "" {
		_ = catalog.RegisterVisible(policy, item)
		return
	}
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, item)
}
