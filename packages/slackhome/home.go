package slackhome

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

const refreshChannel = "slack:home:refresh"

type Publisher interface {
	PublishHome(context.Context, string, map[string]any) error
}

type Controller struct {
	Cfg    config.Config
	Access safety.AccessPolicy
	Slack  Publisher
	Store  userprefs.Store
	Redis  *redisclient.Client
}

func (c Controller) Publish(ctx context.Context, userID string) error {
	if c.Slack == nil || userID == "" {
		return nil
	}
	return c.Slack.PublishHome(ctx, userID, c.View(userID))
}

func (c Controller) RequestRefresh(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if c.Redis == nil {
		return c.Publish(ctx, userID)
	}
	return c.Redis.Publish(ctx, refreshChannel, userID)
}

func (c Controller) StartRefreshSubscriber(ctx context.Context) {
	if c.Redis == nil || c.Slack == nil {
		return
	}
	sub := c.Redis.Subscribe(ctx, refreshChannel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			userID := strings.TrimSpace(msg.Payload)
			if userID == "" {
				continue
			}
			if err := c.Publish(context.Background(), userID); err != nil {
				log.Printf("publish home from refresh request failed user=%s: %v", userID, err)
			}
		}
	}
}

func (c Controller) ToggleWebSearch(ctx context.Context, userID string) {
	if c.Store == nil || userID == "" {
		return
	}
	_ = c.Store.SetWebSearchEnabled(ctx, userID, !c.WebSearchEnabled(userID))
	if err := c.RequestRefresh(context.Background(), userID); err != nil {
		log.Printf("refresh home after web search toggle failed: %v", err)
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
	accessStatus := "Allowed"
	if !allowed {
		accessStatus = "Not allowlisted"
	}

	secondary := strings.TrimSpace(c.Cfg.LLM.SecondaryModel)
	if secondary == "" {
		secondary = c.Cfg.LLM.Model
	}
	webSearchOn := c.WebSearchEnabled(userID)
	webSearchStatus := "On"
	webSearchBtnStyle := "primary"
	if !webSearchOn {
		webSearchStatus = "Off"
		webSearchBtnStyle = ""
	}
	ruleCount := userprefs.CountByKind(context.Background(), c.Store, userID, userprefs.KindRule)
	skillCount := userprefs.CountByKind(context.Background(), c.Store, userID, userprefs.KindSkill)
	statusFields := []map[string]any{
		mrkdwnField("*Access*\n" + accessStatus),
		mrkdwnField("*Web Search*\n" + webSearchStatus),
		mrkdwnField(fmt.Sprintf("*Rules*\n%d active", ruleCount)),
		mrkdwnField(fmt.Sprintf("*Skills*\n%d active", skillCount)),
		mrkdwnField("*Primary Model*\n`" + c.Cfg.LLM.Model + "`"),
		mrkdwnField("*Explorer / Summary*\n`" + secondary + "`"),
	}
	blocks := []map[string]any{
		contextBlock("Mention the agent in a channel or use the Messages tab to start a private thread."),
		dividerBlock(),
		headerBlock(":signal_strength: Status"),
		sectionBlockWithFields("", statusFields...),
		dividerBlock(),
		headerBlock(":control_knobs: Controls"),
		actionsBlock(
			actionButton("manage_rules", "Manage Rules", "rule", ""),
			actionButton("manage_skills", "Manage Skills", "skill", ""),
			actionButton("toggle_user_setting", "Web Search "+boolLabel(webSearchOn), "web_search", webSearchBtnStyle),
		),
	}

	return map[string]any{
		"type":   "home",
		"blocks": blocks,
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

func contextBlock(text string) map[string]any {
	return map[string]any{
		"type": "context",
		"elements": []map[string]any{{
			"type": "mrkdwn",
			"text": text,
		}},
	}
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
