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
func AddToCatalog(catalog *tool.Catalog, policy tool.SurfacePolicy, cfg config.Config, slackClient *slack.Client, reminderStore reminder.Store, rdb *redisclient.Client, conn *connections.Service) error {
	if catalog == nil || slackClient == nil {
		return nil
	}
	registration := tool.NewRegistration(catalog, policy)
	var fileSource FileSearcherSource = BotFileSearcher{Client: slackClient}
	if conn != nil && conn.Config.SlackEnabled() {
		fileSource = ConnectedFileSearcher{Service: *conn}
	}
	fileTool := FileSearchTool{Source: fileSource, Slack: slackClient}
	jsonTool := JSONAnalyzeTool{Source: fileSource, Slack: slackClient}
	registration.Visible(AskUserTool{Slack: slackClient})
	registration.Visible(fileTool)
	registration.Visible(jsonTool)
	if conn != nil && conn.Config.SlackEnabled() {
		attribution := slackmessaging.Attribution{
			BotUserID: cfg.Slack.BotUserID,
			Name:      cfg.Slack.AttributionName,
			Footer:    cfg.Slack.ReplyFooter,
		}
		registration.Visible(UserPostMessageTool{Source: ConnectedClientSource{Service: *conn}, Attribution: attribution})
		registration.Visible(UserReadThreadTool{Source: PreferConnectedThreadReader{
			Connected: ConnectedThreadReader{Service: *conn},
			Bot:       BotThreadReader{Slack: slackClient},
		}})
	} else {
		registration.Visible(UserReadThreadTool{Source: BotThreadReader{Slack: slackClient}})
	}
	registration.Deferred(tool.CategoryIntegration, CreateCanvasTool{Slack: slackClient})
	if reminderStore != nil {
		registration.Visible(bindSurface(reminderTools.CreateTool{
			Store: reminderStore,
			OnCreate: func(ctx context.Context) {
				if rdb != nil {
					_ = rdb.Publish(ctx, "reminders:new", "1")
				}
			},
		}, "reminder"))
		registration.Visible(bindSurface(reminderTools.ListTool{Store: reminderStore}, "reminder"))
		registration.Visible(bindSurface(reminderTools.CancelTool{Store: reminderStore}, "reminder"))
	}
	registerTTS(registration, cfg, slackClient)
	return registration.Err()
}

func registerTTS(registration *tool.Registration, cfg config.Config, slackClient *slack.Client) {
	tts := cfg.Integrations.TTS
	item := bindSurface(ttsTools.SpeakTool{
		Slack:   slackClient,
		APIKey:  tts.APIKey,
		BaseURL: tts.BaseURL,
		Model:   tts.Model,
	}, "tts")
	if tts.APIKey != "" {
		registration.Visible(item)
		return
	}
	registration.Deferred(tool.CategoryIntegration, item)
}
