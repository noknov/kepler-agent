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
	if usageText, ok := s.tokenUsageText(); ok {
		blocks = append(blocks, sectionBlock("*Usage*\n"+usageText))
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

func sectionBlock(text string) map[string]any {
	return map[string]any{
		"type": "section",
		"text": map[string]any{
			"type": "mrkdwn",
			"text": text,
		},
	}
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

func dividerBlock() map[string]any {
	return map[string]any{"type": "divider"}
}

func (s *Server) tokenUsageText() (string, bool) {
	if s.tokenUsage != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if usage, err := s.tokenUsage.Summary(ctx, time.Now()); err == nil {
			return formatTokenUsageText(usage), true
		}
	}
	return "", false
}

func formatTokenUsageText(usage tokenUsageSummary) string {
	lines := make([]string, 0, 3)
	if usage.Rolling != nil {
		lines = append(lines, formatTokenUsageLine("5h", usage.Rolling))
	}
	if usage.Weekly != nil {
		lines = append(lines, formatTokenUsageLine("Week", usage.Weekly))
	}
	if usage.Monthly != nil {
		lines = append(lines, formatTokenUsageLine("Month", usage.Monthly))
	}
	return strings.Join(lines, "\n")
}

const tokenUsageBarWidth = 5

func formatTokenUsageLine(label string, window *tokenUsageWindow) string {
	used := int(math.Round(window.UsagePercent))
	bar := formatTokenUsageBar(used)
	return fmt.Sprintf("%s %d%%/%s · resets in %s", bar, used, label, formatTokenUsageResetIn(window.ResetInSec))
}

func formatTokenUsageBar(percentUsed int) string {
	const subLevels = 8
	total := int(math.Round(float64(percentUsed) / 100 * float64(tokenUsageBarWidth*subLevels)))
	if total > tokenUsageBarWidth*subLevels {
		total = tokenUsageBarWidth * subLevels
	}
	if total < 0 {
		total = 0
	}

	fullCells := total / subLevels
	partialLevel := total % subLevels
	emptyCells := tokenUsageBarWidth - fullCells
	if partialLevel > 0 {
		emptyCells--
	}

	// Vertical block elements from thinnest (1/8) to full (8/8).
	partialChars := []rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

	var b strings.Builder
	b.WriteString(strings.Repeat("█", fullCells))
	if partialLevel > 0 {
		b.WriteRune(partialChars[partialLevel-1])
	}
	b.WriteString(strings.Repeat("░", emptyCells))
	return b.String()
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
