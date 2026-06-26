package app

import (
	"context"
	"fmt"
	"log"
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
	statusEmoji := ":heavy_check_mark:"
	statusText := "Allowed"
	if !allowed {
		statusEmoji = ":no_entry:"
		statusText = "Not allowlisted"
	}

	mentionText := "mention the bot"
	if s.cfg.Slack.BotUserID != "" {
		mentionText = fmt.Sprintf("mention `<@%s>`", s.cfg.Slack.BotUserID)
	}

	modelFields := []map[string]any{
		mrkdwnField("*主模型*\n`" + s.cfg.LLM.Model + "`"),
	}
	secondary := strings.TrimSpace(s.cfg.LLM.SecondaryModel)
	if secondary == "" {
		secondary = s.cfg.LLM.Model
	}
	modelFields = append(modelFields, mrkdwnField("*副模型*\n`"+secondary+"`"))

	blocks := []map[string]any{
		headerBlock("斗包"),
		sectionBlock(fmt.Sprintf("*Access* %s %s", statusEmoji, statusText)),
		sectionBlockWithFields("", modelFields...),
	}
	blocks = append(blocks,
		dividerBlock(),
		sectionBlock(strings.Join([]string{
			"*How to use*",
			"- In a channel: " + mentionText + " and ask your question.",
			"- Continue in the same thread so the bot keeps session context.",
		}, "\n")),
	)

	return map[string]any{
		"type":   "home",
		"blocks": blocks,
	}
}

func mrkdwnField(text string) map[string]any {
	return map[string]any{
		"type": "mrkdwn",
		"text": text,
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

func sectionBlockWithFields(text string, fields ...map[string]any) map[string]any {
	block := map[string]any{"type": "section"}
	if len(fields) > 0 {
		block["fields"] = fields
	}
	if text != "" {
		block["text"] = map[string]any{
			"type": "mrkdwn",
			"text": text,
		}
	}
	return block
}

func actionsBlock(elements ...map[string]any) map[string]any {
	return map[string]any{
		"type":     "actions",
		"elements": elements,
	}
}

func modelSelectMenu(models []string, current string) map[string]any {
	options := make([]map[string]any, len(models))
	var initial map[string]any
	for i, m := range models {
		opt := map[string]any{
			"text":  map[string]any{"type": "plain_text", "text": m},
			"value": m,
		}
		options[i] = opt
		if m == current {
			initial = opt
		}
	}
	menu := map[string]any{
		"type":      "static_select",
		"action_id": "select_model",
		"options":   options,
	}
	if initial != nil {
		menu["initial_option"] = initial
	}
	return menu
}

func sectionBlock(text string) map[string]any {
	return sectionBlockWithFields(text)
}

func dividerBlock() map[string]any {
	return map[string]any{"type": "divider"}
}
