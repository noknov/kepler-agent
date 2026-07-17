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
	accessHeader := "Access  :heavy_check_mark:"
	if !allowed {
		accessHeader = "Access  :no_entry:  Not allowlisted"
	}

	botMention := "the bot"
	if s.cfg.Slack.BotUserID != "" {
		botMention = fmt.Sprintf("<@%s>", s.cfg.Slack.BotUserID)
	}

	secondary := strings.TrimSpace(s.cfg.LLM.SecondaryModel)
	if secondary == "" {
		secondary = s.cfg.LLM.Model
	}
	modelFields := []map[string]any{
		mrkdwnField("*Primary*\n`" + s.cfg.LLM.Model + "`"),
		mrkdwnField("*Explorer / Summary*\n`" + secondary + "`"),
	}

	webSearchOn := s.webSearchPreference(userID)
	webSearchStatus := ":large_green_circle:  On"
	webSearchBtnStyle := "primary"
	if !webSearchOn {
		webSearchStatus = ":white_circle:  Off"
		webSearchBtnStyle = ""
	}

	return map[string]any{
		"type": "home",
		"blocks": []map[string]any{
			sectionBlock(botMention + " — chat in any channel or DM"),
			headerBlock(accessHeader),
			dividerBlock(),
			headerBlock("Model"),
			sectionBlockWithFields("", modelFields...),
			dividerBlock(),
			headerBlock("Web Search"),
			sectionBlockWithAccessory(
				"*Auto-search*\n"+webSearchStatus,
				toggleButton("toggle_web_search", boolLabel(webSearchOn), webSearchBtnStyle),
			),
			dividerBlock(),
		},
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

func boolLabel(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

func sectionBlock(text string) map[string]any {
	return sectionBlockWithFields(text)
}

func sectionBlockWithAccessory(text string, accessory map[string]any) map[string]any {
	return map[string]any{
		"type": "section",
		"text": map[string]any{
			"type": "mrkdwn",
			"text": text,
		},
		"accessory": accessory,
	}
}

func toggleButton(actionID, label, style string) map[string]any {
	btn := map[string]any{
		"type":      "button",
		"action_id": actionID,
		"text": map[string]any{
			"type":  "plain_text",
			"text":  label,
			"emoji": true,
		},
		"value": label,
	}
	if style != "" {
		btn["style"] = style
	}
	return btn
}

func dividerBlock() map[string]any {
	return map[string]any{"type": "divider"}
}
