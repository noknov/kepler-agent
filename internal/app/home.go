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
		headerBlock("Oncall Agent"),
		fieldsBlock(
			field("*Access*\n"+statusEmoji+" "+statusText),
			field("*Model*\n`"+s.cfg.LLM.Model+"`"),
		),
		dividerBlock(),
		sectionBlock(strings.Join([]string{
			"*How to use*",
			"- In a channel: " + mentionText + " and ask your question.",
			"- Continue in the same thread so the bot keeps session context.",
		}, "\n")),
		dividerBlock(),
		sectionBlock(strings.Join([]string{
			"*更新日志*",
			"*2026-06-03*",
			"- 每次回答都会保存一份运行记录，方便之后排查问题、看 token 用量和大致成本。",
			"- 可能读取本机敏感内容、生产日志，或触发外部动作时，会先请你在 Slack 线程里确认。",
			"- 你对回答点的表情反馈会记录到对应回答上，后续可以用来判断效果好不好。",
			"- 新增排障简报能力，可以先帮你把 on-call 调查整理成清晰步骤。",
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
