package slackagent

import (
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

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

	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	started, err := s.ensureNativeStream(chunks)
	if err != nil {
		return
	}
	if started {
		return
	}

	s.mu.Lock()
	messageTS := s.messageTS
	deliveryFailed := s.streamDeliveryFailed
	s.mu.Unlock()
	if messageTS == "" || deliveryFailed {
		return
	}
	native, ok := s.messenger.(slackconversation.NativeStreamMessenger)
	if !ok {
		return
	}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if err := native.AppendStream(ctx, s.req.Channel, messageTS, chunks); err != nil {
		s.mu.Lock()
		s.streamDeliveryFailed = true
		s.mu.Unlock()
	}
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
