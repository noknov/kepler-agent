package conversation

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/prompts"
)

type progressAppender func([]map[string]any)

type activeRun struct {
	mu             sync.Mutex
	sessionID      string
	userID         string
	cancel         context.CancelFunc
	canceled       bool
	queued         []Request
	consumed       []Request
	appendProgress progressAppender
	locale         string
}

func newActiveRun(sessionID, userID string, cancel context.CancelFunc) *activeRun {
	return &activeRun{
		sessionID: sessionID,
		userID:    userID,
		cancel:    cancel,
	}
}

func (a *activeRun) setProgress(locale string, appendProgress progressAppender) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.locale = locale
	a.appendProgress = appendProgress
}

func (a *activeRun) enqueue(req Request) {
	a.mu.Lock()
	a.queued = append(a.queued, req)
	a.mu.Unlock()
}

func (a *activeRun) interrupt() {
	a.mu.Lock()
	if a.canceled {
		a.mu.Unlock()
		return
	}
	a.canceled = true
	cancel := a.cancel
	appendProgress := a.appendProgress
	locale := a.locale
	a.mu.Unlock()

	if appendProgress != nil {
		appendProgress([]map[string]any{
			{"type": "task_update", "id": "thinking", "title": agent.CancelingTitle(locale), "status": "in_progress"},
		})
	}
	if cancel != nil {
		cancel()
	}
}

func (a *activeRun) wasCanceled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.canceled
}

func (a *activeRun) drainMessages() []llm.Message {
	a.mu.Lock()
	queued := append([]Request(nil), a.queued...)
	a.queued = nil
	a.consumed = append(a.consumed, queued...)
	appendProgress := a.appendProgress
	locale := a.locale
	a.mu.Unlock()

	if len(queued) == 0 {
		return nil
	}
	if appendProgress != nil {
		appendProgress([]map[string]any{
			{"type": "task_update", "id": "thinking", "title": agent.SteeringQueuedTitle(locale), "status": "in_progress"},
			{"type": "markdown_text", "text": steeringAppliedMessage(locale)},
		})
	}
	content := formatSteeringMessages(queued)
	msg := llm.Message{
		Role:    "user",
		Content: content,
	}
	parts := []llm.ContentPart{llm.TextPart(content)}
	for _, req := range queued {
		parts = append(parts, req.ContentParts...)
	}
	if len(parts) > 1 {
		msg.ContentParts = parts
	}
	return []llm.Message{msg}
}

func (a *activeRun) consumedRequests() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Request(nil), a.consumed...)
}

func (a *activeRun) remainingQueued() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]Request(nil), a.queued...)
	a.queued = nil
	return out
}

func formatSteeringMessages(requests []Request) string {
	var b strings.Builder
	b.WriteString(prompts.PromptText("steering_header", ""))
	b.WriteString("<active_turn_guidance>\n")
	for _, req := range requests {
		text := strings.TrimSpace(req.Text)
		if text == "" {
			continue
		}
		b.WriteString("- <@")
		b.WriteString(req.UserID)
		b.WriteString(">: ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	b.WriteString("</active_turn_guidance>")
	return b.String()
}

func combineQueuedFollowUps(requests []Request) (Request, bool) {
	if len(requests) == 0 {
		return Request{}, false
	}
	first := requests[0]
	var b strings.Builder
	b.WriteString("Queued follow-up from messages sent while the previous run was finishing:\n")
	var combinedParts []llm.ContentPart
	for _, req := range requests {
		text := strings.TrimSpace(req.Text)
		if text != "" {
			b.WriteString("- <@")
			b.WriteString(req.UserID)
			b.WriteString(">: ")
			b.WriteString(text)
			b.WriteString("\n")
		}
		combinedParts = append(combinedParts, req.ContentParts...)
	}
	followUp := first
	followUp.EventID = first.EventID + ":queued:" + time.Now().UTC().Format("150405.000000000")
	followUp.Text = strings.TrimSpace(b.String())
	followUp.ContentParts = combinedParts
	if followUp.Text == "" && len(combinedParts) == 0 {
		return Request{}, false
	}
	return followUp, true
}

func isCancelRequest(text string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	text = strings.Trim(text, " .!！。")
	if text == "" {
		return false
	}
	switch text {
	case "cancel", "stop", "abort", "interrupt", "中止", "停止", "取消":
		return true
	default:
		return false
	}
}

func interruptedMessage(locale string) string {
	if locale == agent.LocaleZH {
		return "已中止本次请求。"
	}
	return "Cancelled this request."
}

func steeringAppliedMessage(locale string) string {
	if locale == agent.LocaleZH {
		return "\n\n_已引导对话_\n"
	}
	return "\n\n_Conversation Steered_\n"
}
