package slacktool

import (
	"context"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/noknov/kepler-agent/packages/reminder"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	slackmessaging "github.com/noknov/kepler-agent/packages/surfaces/slack/messaging"
	reminderTools "github.com/noknov/kepler-agent/packages/tools/reminder"
	ttsTools "github.com/noknov/kepler-agent/packages/tools/tts"
)

// AddToCatalog registers Slack-surface tools for the hosted worker only.
// Messaging and connection tools stay here; they are not exposed through CLI.
func AddToCatalog(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, rdb *redisclient.Client, conn *connections.Service) {
	if catalog == nil || slackClient == nil {
		return
	}
	var fileSource FileSearcherSource = BotFileSearcher{Client: slackClient}
	if conn != nil && conn.Config.SlackEnabled() {
		fileSource = ConnectedFileSearcher{Service: *conn}
	}
	fileTool := FileSearchTool{Source: fileSource, Slack: slackClient}
	jsonTool := JSONAnalyzeTool{Source: fileSource, Slack: slackClient}
	_ = catalog.RegisterVisible(policy, AskUserTool{Slack: slackClient})
	_ = catalog.RegisterVisible(policy, fileTool)
	_ = catalog.RegisterVisible(policy, jsonTool)
	if conn != nil && conn.Config.SlackEnabled() {
		attribution := slackmessaging.Attribution{
			BotUserID: cfg.Slack.BotUserID,
			Name:      cfg.Slack.AttributionName,
			Footer:    cfg.Slack.ReplyFooter,
		}
		_ = catalog.RegisterVisible(policy, UserPostMessageTool{Source: ConnectedClientSource{Service: *conn}, Attribution: attribution})
		_ = catalog.RegisterVisible(policy, UserReadThreadTool{Source: PreferConnectedThreadReader{
			Connected: ConnectedThreadReader{Service: *conn},
			Bot:       BotThreadReader{Slack: slackClient},
		}})
	} else {
		_ = catalog.RegisterVisible(policy, UserReadThreadTool{Source: BotThreadReader{Slack: slackClient}})
	}
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
