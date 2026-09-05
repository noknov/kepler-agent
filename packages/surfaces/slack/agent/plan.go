package slackagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

const planUpdateInterval = 3 * time.Second

// UpdatePlan renders model-authored execution plans using Slack's documented
// task_update chunks in plan display mode. It deliberately does not infer plan
// state from individual tool calls.
func (s *slackStream) UpdatePlan(plan *tool.PlanUpdate) {
	if plan == nil || len(plan.Items) == 0 {
		return
	}
	chunks := planChunks(plan)
	if len(chunks) == 0 {
		return
	}

	s.schedulePlanUpdate(plan)
}

func (s *slackStream) schedulePlanUpdate(plan *tool.PlanUpdate) {
	blocksMessenger, canPost := s.messenger.(slackconversation.ApprovalMessenger)
	blocksUpdater, canUpdate := s.messenger.(slackconversation.MessageBlocksUpdater)
	if !canPost || !canUpdate {
		return
	}

	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	if s.streamClosed {
		s.mu.Unlock()
		return
	}
	if s.planMessageTS != "" && time.Since(s.planLastUpdate) < planUpdateInterval {
		pending := clonePlan(plan)
		remaining := planUpdateInterval - time.Since(s.planLastUpdate)
		if s.planTimer != nil {
			s.planTimer.Stop()
		}
		s.planTimer = time.AfterFunc(remaining, func() {
			s.deliveryMu.Lock()
			defer s.deliveryMu.Unlock()
			s.mu.Lock()
			if s.streamClosed {
				s.mu.Unlock()
				return
			}
			s.planTimer = nil
			s.mu.Unlock()
			s.deliverPlan(blocksMessenger, blocksUpdater, pending)
		})
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.deliverPlan(blocksMessenger, blocksUpdater, plan)
}

func (s *slackStream) deliverPlan(messenger slackconversation.ApprovalMessenger, updater slackconversation.MessageBlocksUpdater, plan *tool.PlanUpdate) {
	s.mu.Lock()
	s.planRevision++
	revision := s.planRevision
	messageTS := s.planMessageTS
	s.mu.Unlock()
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
			return
		}
		s.mu.Lock()
		s.planMessageTS = ts
		s.planLastUpdate = time.Now()
		s.mu.Unlock()
		return
	}
	if err := updater.UpdateMessageBlocks(ctx, s.req.Channel, messageTS, text, blocks); err != nil {
		return
	}
	s.mu.Lock()
	s.planLastUpdate = time.Now()
	s.mu.Unlock()
}

func clonePlan(plan *tool.PlanUpdate) *tool.PlanUpdate {
	copyPlan := *plan
	copyPlan.Items = append([]tool.PlanItem(nil), plan.Items...)
	return &copyPlan
}

func planChunks(plan *tool.PlanUpdate) []map[string]any {
	chunks := make([]map[string]any, 0, len(plan.Items)+1)
	title := strings.TrimSpace(plan.Explanation)
	if title == "" {
		title = "Execution plan"
	}
	chunks = append(chunks, map[string]any{"type": "plan_update", "title": truncatePlanField(title)})
	for index, item := range plan.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("task_%d", index+1)
		}
		chunk := map[string]any{
			"type":   "task_update",
			"id":     id,
			"title":  truncatePlanField(item.Task),
			"status": slackPlanStatus(item.Status),
		}
		if note := strings.TrimSpace(item.Note); note != "" {
			chunk["details"] = truncatePlanField(note)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
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
