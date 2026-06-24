package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

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

	currentModel := s.cfg.LLM.Model
	if v, ok := s.modelPrefs.Load(userID); ok {
		currentModel = v.(string)
	}

	models := s.cfg.LLM.AvailableModels

	blocks := []map[string]any{
		headerBlock("斗包"),
		sectionBlock(fmt.Sprintf("*Access* %s\n%s", statusText, statusEmoji)),
		sectionBlock("*Model* `" + currentModel + "`"),
	}
	if len(models) > 1 {
		blocks = append(blocks, actionsBlock(modelSelectMenu(models, currentModel)))
	}
	if block, ok := s.tokenUsageSectionBlock(); ok {
		blocks = append(blocks, block)
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

func (s *Server) tokenUsageSectionBlock() (map[string]any, bool) {
	if s.tokenUsage == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	usage, err := s.tokenUsage.Summary(ctx, time.Now())
	if err != nil {
		return nil, false
	}
	text, ok := formatTokenUsageText(usage)
	if !ok {
		return nil, false
	}
	return sectionBlock(text), true
}

type tokenUsageRow struct {
	label  string
	window *tokenUsageWindow
}

func formatTokenUsageText(usage tokenUsageSummary) (string, bool) {
	rows := make([]tokenUsageRow, 0, 3)
	if usage.Rolling != nil {
		rows = append(rows, tokenUsageRow{"5h", usage.Rolling})
	}
	if usage.Weekly != nil {
		rows = append(rows, tokenUsageRow{"Week", usage.Weekly})
	}
	if usage.Monthly != nil {
		rows = append(rows, tokenUsageRow{"Month", usage.Monthly})
	}
	if len(rows) == 0 {
		return "", false
	}

	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, "*Usage*")
	for _, row := range rows {
		used := int(math.Round(row.window.UsagePercent))
		lines = append(lines, fmt.Sprintf("%s *%s* · %d%% · _resets in %s_",
			tokenUsageStatusEmoji(used),
			row.label,
			used,
			formatTokenUsageResetIn(row.window.ResetInSec),
		))
	}
	return strings.Join(lines, "\n"), true
}

func tokenUsageStatusEmoji(percent int) string {
	switch {
	case percent >= 80:
		return ":red_circle:"
	case percent >= 50:
		return ":large_yellow_circle:"
	default:
		return ":large_green_circle:"
	}
}

func formatTokenUsageResetIn(seconds int64) string {
	if seconds <= 0 {
		return "now"
	}
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60

	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
