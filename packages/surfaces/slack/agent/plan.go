package slackagent

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

const planUpdateInterval = 3 * time.Second

// UpdatePlan renders model-authored execution plans using Slack's native plan block.
// It deliberately does not infer plan state from individual tool calls.
func (s *slackStream) UpdatePlan(plan *tool.PlanUpdate) {
	if plan == nil || len(plan.Items) == 0 {
		return
	}
	s.schedulePlanUpdate(plan)
}

// Plans are a latest-value projection of durable PlanUpdated events. Keep one
// pending snapshot and one delivery, so Slack backpressure never blocks the
// runtime or builds an unbounded queue of obsolete progress updates.
func (s *slackStream) schedulePlanUpdate(plan *tool.PlanUpdate) {
	if _, ok := s.messenger.(slackconversation.ApprovalMessenger); !ok {
		return
	}
	if _, ok := s.messenger.(slackconversation.MessageBlocksUpdater); !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.planClosing {
		return
	}
	s.pendingPlan = clonePlan(plan)
	s.schedulePlanUpdateLocked()
}

// Caller holds mu. The timer remains registered while its delivery is in flight.
func (s *slackStream) schedulePlanUpdateLocked() {
	if s.planClosing || s.planTimer != nil || s.pendingPlan == nil {
		return
	}
	delay := max(time.Duration(0), planUpdateInterval-time.Since(s.planLastUpdate))
	s.planTimer = time.AfterFunc(delay, func() {
		s.flushDeferredPlan(false)
		s.mu.Lock()
		s.planTimer = nil
		s.schedulePlanUpdateLocked()
		s.mu.Unlock()
	})
}

func (s *slackStream) flushDeferredPlan(final bool) {
	// Plans use their own delivery lock: a slow plan must not hold up answer
	// streaming, and older snapshots must never overwrite newer ones.
	s.planDeliveryMu.Lock()
	defer s.planDeliveryMu.Unlock()
	s.mu.Lock()
	if s.planClosing && !final {
		s.mu.Unlock()
		return
	}
	plan := s.pendingPlan
	s.pendingPlan = nil
	s.mu.Unlock()
	if plan == nil {
		return
	}
	s.deliverPlan(s.messenger.(slackconversation.ApprovalMessenger), s.messenger.(slackconversation.MessageBlocksUpdater), plan)
	s.mu.Lock()
	// Also throttle after a failed attempt; only a new snapshot triggers another
	// delivery. Non-idempotent posts are not retried by this scheduler.
	s.planLastUpdate = time.Now()
	s.mu.Unlock()
}

// finishPlan is the final delivery barrier, including when the turn failed or
// was canceled. Deliver the last authored state without inventing completion.
func (s *slackStream) finishPlan() {
	s.mu.Lock()
	s.planClosing = true
	if s.planTimer != nil {
		s.planTimer.Stop()
	}
	s.mu.Unlock()
	s.flushDeferredPlan(true)
}

func (s *slackStream) deliverPlan(messenger slackconversation.ApprovalMessenger, updater slackconversation.MessageBlocksUpdater, plan *tool.PlanUpdate) {
	s.mu.Lock()
	s.planRevision++
	revision := s.planRevision
	messageTS := s.planMessageTS
	uncertain := s.planPostUncertain
	s.mu.Unlock()
	if uncertain {
		return
	}
	blocks := planBlocks(plan, revision)
	if len(blocks) == 0 {
		return
	}
	text := planFallbackText(plan)
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if messageTS == "" {
		ts, err := messenger.PostMessageBlocks(ctx, s.req.Channel, s.req.ThreadTS, text, blocks)
		if err != nil {
			s.mu.Lock()
			s.planPostUncertain = isDeliveryUncertain(err)
			s.mu.Unlock()
			log.Printf("slack plan post failed channel=%s thread=%s: %v", s.req.Channel, s.req.ThreadTS, err)
			return
		}
		s.mu.Lock()
		s.planMessageTS = ts
		s.mu.Unlock()
		return
	}
	if err := updater.UpdateMessageBlocks(ctx, s.req.Channel, messageTS, text, blocks); err != nil {
		log.Printf("slack plan update failed channel=%s ts=%s: %v", s.req.Channel, messageTS, err)
		return
	}
}

func clonePlan(plan *tool.PlanUpdate) *tool.PlanUpdate {
	copyPlan := *plan
	copyPlan.Items = append([]tool.PlanItem(nil), plan.Items...)
	return &copyPlan
}

func planBlocks(plan *tool.PlanUpdate, revision int) []map[string]any {
	if plan == nil || len(plan.Items) == 0 {
		return nil
	}
	tasks := make([]map[string]any, 0, len(plan.Items))
	for index, item := range plan.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("task_%d", index+1)
		}
		task := map[string]any{
			"task_id": id,
			"title":   truncatePlanField(item.Task),
			"status":  slackPlanStatus(item.Status),
		}
		if note := strings.TrimSpace(item.Note); note != "" {
			task["details"] = richText(note)
		}
		tasks = append(tasks, task)
	}
	title := strings.TrimSpace(plan.Explanation)
	if title == "" {
		title = "Execution plan"
	}
	return []map[string]any{{
		"type":     "plan",
		"block_id": fmt.Sprintf("agent_plan_%d", revision),
		"title":    truncatePlanField(title),
		"tasks":    tasks,
	}}
}

func richText(text string) map[string]any {
	return map[string]any{
		"type": "rich_text",
		"elements": []map[string]any{{
			"type":     "rich_text_section",
			"elements": []map[string]any{{"type": "text", "text": truncatePlanField(text)}},
		}},
	}
}

func planFallbackText(plan *tool.PlanUpdate) string {
	if plan == nil {
		return "Execution plan"
	}
	if title := strings.TrimSpace(plan.Explanation); title != "" {
		return truncatePlanField(title)
	}
	return "Execution plan"
}

func slackPlanStatus(status string) string {
	switch status {
	case "completed":
		return "complete"
	case "blocked":
		return "error"
	default:
		return status
	}
}

func truncatePlanField(value string) string {
	const maxRunes = 256
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
