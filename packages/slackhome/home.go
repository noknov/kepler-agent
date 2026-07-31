package slackhome

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

type Controller struct {
	Cfg    config.Config
	Access safety.AccessPolicy
	Slack  *slack.Client
	Store  userprefs.Store
}

func (c Controller) Publish(ctx context.Context, userID string) error {
	if c.Slack == nil || userID == "" {
		return nil
	}
	return c.Slack.PublishHome(ctx, userID, c.View(userID))
}

func (c Controller) ToggleWebSearch(ctx context.Context, userID string) {
	if c.Store == nil || userID == "" {
		return
	}
	_ = c.Store.SetWebSearchEnabled(ctx, userID, !c.WebSearchEnabled(userID))
	if err := c.Publish(context.Background(), userID); err != nil {
		log.Printf("publish home after web search toggle failed: %v", err)
	}
}

func (c Controller) WebSearchEnabled(userID string) bool {
	if c.Store == nil {
		return true
	}
	settings, err := c.Store.GetSettings(context.Background(), userID)
	if err != nil {
		return true
	}
	return settings.WebSearchEnabled
}

func (c Controller) View(userID string) map[string]any {
	allowed := c.Access.AllowsUser(userID)
	accessHeader := "Access  :heavy_check_mark:"
	if !allowed {
		accessHeader = "Access  :no_entry:  Not allowlisted"
	}

	botMention := "the bot"
	if c.Cfg.Slack.BotUserID != "" {
		botMention = fmt.Sprintf("<@%s>", c.Cfg.Slack.BotUserID)
	}

	secondary := strings.TrimSpace(c.Cfg.LLM.SecondaryModel)
	if secondary == "" {
		secondary = c.Cfg.LLM.Model
	}
	modelFields := []map[string]any{
		mrkdwnField("*Primary*\n`" + c.Cfg.LLM.Model + "`"),
		mrkdwnField("*Explorer / Summary*\n`" + secondary + "`"),
	}

	webSearchOn := c.WebSearchEnabled(userID)
	webSearchStatus := ":large_green_circle:  On"
	webSearchBtnStyle := "primary"
	if !webSearchOn {
		webSearchStatus = "Off  :white_circle:"
		webSearchBtnStyle = ""
	} else {
		webSearchStatus = "On  :large_green_circle:"
	}
	ruleCount := userprefs.CountByKind(context.Background(), c.Store, userID, userprefs.KindRule)
	skillCount := userprefs.CountByKind(context.Background(), c.Store, userID, userprefs.KindSkill)

	return map[string]any{
		"type": "home",
		"blocks": []map[string]any{
			sectionBlock(botMention + " — chat in any channel or DM"),
			headerBlock(accessHeader),
			dividerBlock(),
			headerBlock("Model"),
			sectionBlockWithFields("", modelFields...),
			dividerBlock(),
			headerBlock("Personalization"),
			sectionBlockWithAccessory(
				fmt.Sprintf("*Rules*  %d active", ruleCount),
				actionButton("manage_rules", "Manage Rules", "rule", ""),
			),
			sectionBlockWithAccessory(
				fmt.Sprintf("*Skills*  %d active", skillCount),
				actionButton("manage_skills", "Manage Skills", "skill", ""),
			),
			dividerBlock(),
			headerBlock("Settings"),
			sectionBlockWithAccessory(
				"*Web Search*  "+webSearchStatus,
				actionButton("toggle_user_setting", boolLabel(webSearchOn), "web_search", webSearchBtnStyle),
			),
			dividerBlock(),
		},
	}
}

func mrkdwnField(text string) map[string]any {
	return map[string]any{"type": "mrkdwn", "text": text}
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
		block["text"] = map[string]any{"type": "mrkdwn", "text": text}
	}
	return block
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
		"type":      "section",
		"text":      map[string]any{"type": "mrkdwn", "text": text},
		"accessory": accessory,
	}
}

func actionsBlock(elements ...map[string]any) map[string]any {
	return map[string]any{
		"type":     "actions",
		"elements": elements,
	}
}

func actionButton(actionID, label, value, style string) map[string]any {
	btn := map[string]any{
		"type":      "button",
		"action_id": actionID,
		"text": map[string]any{
			"type":  "plain_text",
			"text":  label,
			"emoji": true,
		},
		"value": value,
	}
	if style != "" {
		btn["style"] = style
	}
	return btn
}

func dividerBlock() map[string]any {
	return map[string]any{"type": "divider"}
}
