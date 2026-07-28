package slackhome

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/slack"
)

type Controller struct {
	Cfg    config.Config
	Access safety.AccessPolicy
	Redis  *redisclient.Client
	Slack  *slack.Client
}

func (c Controller) Publish(ctx context.Context, userID string) error {
	if c.Slack == nil || userID == "" {
		return nil
	}
	return c.Slack.PublishHome(ctx, userID, c.View(userID))
}

func (c Controller) ToggleWebSearch(ctx context.Context, userID string) {
	if c.Redis == nil || userID == "" {
		return
	}
	_ = c.Redis.SetBool(ctx, WebSearchKey(userID), !c.WebSearchEnabled(userID))
	if err := c.Publish(context.Background(), userID); err != nil {
		log.Printf("publish home after web search toggle failed: %v", err)
	}
}

func (c Controller) WebSearchEnabled(userID string) bool {
	if c.Redis == nil {
		return true
	}
	return c.Redis.GetBool(context.Background(), WebSearchKey(userID), true)
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

func WebSearchKey(userID string) string {
	return "websearch:" + userID
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
