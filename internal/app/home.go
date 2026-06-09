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

	mentionText := "mention the bot"
	if s.cfg.Slack.BotUserID != "" {
		mentionText = fmt.Sprintf("mention `<@%s>`", s.cfg.Slack.BotUserID)
	}

	blocks := []map[string]any{
		headerBlock("Channel-X Copilot Agent"),
		fieldsBlock(
			field("*Access*\n"+statusEmoji+" "+statusText),
			field("*Provider*\n`"+emptyDash(s.cfg.LLM.Provider)+"`"),
			field("*Model*\n`"+s.cfg.LLM.Model+"`"),
			field("*Protocol*\n`"+emptyDash(s.cfg.LLM.Protocol)+"`"),
			field("*Base URL*\n`"+baseURLHost(s.cfg.LLM.BaseURL)+"`"),
			field("*Anthropic flavor*\n`"+emptyDash(s.cfg.LLM.AnthropicFlavor)+"`"),
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

func baseURLHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return emptyDash(raw)
	}
	return parsed.Host
}

func emptyDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
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
