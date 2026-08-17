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
	_ = catalog.RegisterVisible(policy, tool.Annotate(AskUserTool{Slack: slackClient}, tool.Descriptor{
		Effects:      []tool.Effect{tool.EffectRead, tool.EffectNetwork},
		Dependencies: []string{"slack"},
		Exclusive:    true,
	}))
	_ = catalog.RegisterVisible(policy, readTool(FileSearchTool{Slack: slackClient}, "slack"))
	_ = catalog.RegisterVisible(policy, readTool(JSONAnalyzeTool{Slack: slackClient}, "slack"))
	_ = catalog.RegisterDeferredVisible(policy, tool.CategoryIntegration, externalWrite(CreateCanvasTool{Slack: slackClient}))
	if reminderStore != nil {
		_ = catalog.RegisterVisible(policy, externalWrite(reminderTools.CreateTool{
			Store: reminderStore,
			OnCreate: func(ctx context.Context) {
				if rdb != nil {
					_ = rdb.Publish(ctx, "reminders:new", "1")
				}
			},
		}, "reminder"))
		_ = catalog.RegisterVisible(policy, readTool(reminderTools.ListTool{Store: reminderStore}, "reminder"))
		_ = catalog.RegisterVisible(policy, externalWrite(reminderTools.CancelTool{Store: reminderStore}, "reminder"))
	}
	registerTTS(catalog, policy, cfg, slackClient)
}

func readTool(item tool.Tool, deps ...string) tool.Tool {
	return tool.Annotate(item, tool.Descriptor{Effects: []tool.Effect{tool.EffectRead, tool.EffectNetwork}, Parallel: true, Dependencies: deps})
}

func externalWrite(item tool.Tool, deps ...string) tool.Tool {
	return tool.Annotate(item, tool.Descriptor{
		Effects:      []tool.Effect{tool.EffectExternalWrite, tool.EffectNetwork},
		Dependencies: append([]string{"slack"}, deps...),
		Surfaces:     []string{"slack"},
	})
}

func registerTTS(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, slackClient *slack.Client) {
	tts := cfg.Integrations.TTS
	item := externalWrite(ttsTools.SpeakTool{
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
