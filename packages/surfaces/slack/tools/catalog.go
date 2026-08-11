package slacktool

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
	reminderTools "github.com/noknov/slack-copilot-agent/packages/tools/reminder"
	ttsTools "github.com/noknov/slack-copilot-agent/packages/tools/tts"
)

func AddToRegistry(reg *registry.Registry, cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, rdb *redisclient.Client) {
	if reg == nil || slackClient == nil {
		return
	}
	reg.Register(runtimeRead(AskUserTool{Slack: slackClient}, "slack"))
	reg.Register(runtimeRead(FileSearchTool{Slack: slackClient}, "slack"))
	reg.Register(runtimeRead(JSONAnalyzeTool{Slack: slackClient}, "slack"))
	registerDeferredTools(reg, registry.CategoryBrowser, slackExternalWrite(SendScreenshotTool{Slack: slackClient}))
	registerDeferredTools(reg, registry.CategoryIntegration, slackExternalWrite(CreateCanvasTool{Slack: slackClient}))
	if reminderStore != nil {
		reg.Register(slackExternalWrite(reminderTools.CreateTool{
			Store: reminderStore,
			OnCreate: func(ctx context.Context) {
				if rdb != nil {
					_ = rdb.Publish(ctx, "reminders:new", "1")
				}
			},
		}, "reminder"))
		reg.Register(runtimeRead(reminderTools.ListTool{Store: reminderStore}, "reminder"))
		reg.Register(slackExternalWrite(reminderTools.CancelTool{Store: reminderStore}, "reminder"))
	}
	registerTTS(reg, cfg, slackClient)
}

func slackExternalWrite(tool registry.Tool, deps ...string) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{
		Risk:         registry.RiskExternalWrite,
		Dependencies: append([]string{"slack"}, deps...),
		Surfaces:     []string{"slack"},
	})
}

func runtimeRead(tool registry.Tool, deps ...string) registry.Tool {
	return registry.WithMetadata(tool, registry.ToolMetadata{
		Risk:         registry.RiskRead,
		Dependencies: deps,
	})
}

func registerTTS(reg *registry.Registry, cfg config.Config, slackClient *slack.Client) {
	tts := cfg.Integrations.TTS
	tool := slackExternalWrite(ttsTools.SpeakTool{
		Slack:   slackClient,
		APIKey:  tts.APIKey,
		BaseURL: tts.BaseURL,
		Model:   tts.Model,
	}, "tts")
	if tts.APIKey != "" {
		reg.Register(tool)
		return
	}
	reg.RegisterDeferred(registry.AsDeferred(registry.CategoryIntegration, tool))
}

func registerDeferredTools(reg *registry.Registry, category string, tools ...registry.Tool) {
	for _, tool := range tools {
		reg.RegisterDeferred(registry.AsDeferred(category, tool))
	}
}
