package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/wati/oncall-agent/internal/slack"
)

func (s *Server) handleAppHome(ctx context.Context, ev slack.Event) {
	if ev.User == "" {
		return
	}
	if ev.Tab != "" && ev.Tab != "home" {
		return
	}
	if err := s.slack.PublishHome(ctx, ev.User, s.homeView(ev.User)); err != nil {
		log.Printf("publish home failed: %v", err)
	}
}

func (s *Server) homeView(userID string) map[string]any {
	allowed := s.access.AllowsUser(userID)
	statusEmoji := ":white_check_mark:"
	statusText := "Allowed"
	if !allowed {
		statusEmoji = ":no_entry:"
		statusText = "Not allowlisted"
	}

	channelMode := "Any channel mention"
	if len(s.cfg.Security.AllowedChannels) > 0 {
		channelMode = "Allowlisted channels only"
	}
	mentionText := "mention the bot"
	if s.cfg.Slack.BotUserID != "" {
		mentionText = fmt.Sprintf("mention `<@%s>`", s.cfg.Slack.BotUserID)
	}

	blocks := []map[string]any{
		headerBlock("Oncall Agent"),
		sectionBlock("*Shared oncall assistant*\nNo personal Cursor API key is needed. This app uses the service-level API credentials plus Slack-side access control."),
		fieldsBlock(
			field("*Access*\n"+statusEmoji+" "+statusText),
			field("*Channel mode*\n"+channelMode),
			field("*Provider*\n"+displayProviderName(s.cfg.LLM.BaseURL)),
			field("*Base URL*\n"+displayProviderHost(s.cfg.LLM.BaseURL)),
			field("*Model*\n`"+s.cfg.LLM.Model+"`"),
		),
		dividerBlock(),
		sectionBlock(strings.Join([]string{
			"*How to use*",
			"- In a channel: " + mentionText + " and ask your question.",
			"- Continue in the same thread so the bot keeps session context.",
		}, "\n")),
	}

	return map[string]any{
		"type":   "home",
		"blocks": blocks,
	}
}

func headerBlock(text string) map[string]any {
	return map[string]any{
		"type": "header",
		"text": map[string]any{
			"type":  "plain_text",
			"text":  text,
			"emoji": true,
		},
	}
}

func sectionBlock(text string) map[string]any {
	return map[string]any{
		"type": "section",
		"text": map[string]any{
			"type": "mrkdwn",
			"text": text,
		},
	}
}

func fieldsBlock(fields ...map[string]any) map[string]any {
	return map[string]any{
		"type":   "section",
		"fields": fields,
	}
}

func field(text string) map[string]any {
	return map[string]any{
		"type": "mrkdwn",
		"text": text,
	}
}

func dividerBlock() map[string]any {
	return map[string]any{"type": "divider"}
}

func displayProviderHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		if rawURL == "" {
			return "Unknown"
		}
		return "`" + rawURL + "`"
	}
	return "`" + parsed.Host + "`"
}

func displayProviderName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "Unknown"
	}
	switch parsed.Host {
	case "api.deepseek.com":
		return "DeepSeek"
	case "api.moonshot.ai", "api.kimi.com":
		return "Kimi"
	default:
		return "`" + parsed.Host + "`"
	}
}
