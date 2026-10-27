package slackhandler

import (
	"context"
	"log"
	"strings"

	"github.com/noknov/kepler-agent/packages/prompts"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func (h *Handler) publishSuggestedPrompts(ctx context.Context, event slack.Event) {
	if h.Slack == nil || !h.Access.AllowsUser(event.User) || !IsDMChannel(event.Channel) {
		return
	}
	language := "en"
	if conversation.IsChineseLocale(h.Cfg.Slack.DefaultLocale) {
		language = "zh"
	}
	text := func(key string) string { return prompts.AppMessage("slack_suggestions_"+language+"_"+key, "") }
	keys := []string{"capabilities", "investigation", "canvas", "attachment"}
	view := selectViewingContext(event.TeamID, event.Context)
	if view != nil {
		keys = []string{"summary", "investigation", "canvas", "actions"}
	}
	entries := make([]slack.SuggestedPrompt, 0, len(keys))
	for _, key := range keys {
		entry := slack.SuggestedPrompt{Title: text(key + "_title"), Message: text(key + "_message")}
		if view != nil {
			entry.Title = strings.ReplaceAll(entry.Title, "{{channel}}", view.ChannelID)
			entry.Message = strings.ReplaceAll(entry.Message, "{{channel}}", view.ChannelID)
		}
		entries = append(entries, entry)
	}
	if err := h.Slack.SetSuggestedPrompts(ctx, event.Channel, text("title"), entries); err != nil {
		// Optional discovery UI must not execute a turn or fail durable event delivery
		// when the workspace has static prompts, missing scopes or disabled agent_view.
		log.Printf("set Slack suggested prompts failed error_type=%T", err)
		if h.Metrics != nil {
			h.Metrics.Event("slack_suggested_prompts_failed", nil)
		}
	}
}
