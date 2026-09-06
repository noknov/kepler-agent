package slackagent

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

const (
	sessionProcessing = "processing"
	sessionActive     = "active"
	sessionSuspended  = "suspended"
)

// Lifecycle projects canonical turn completion into Slack's agent-session
// lifecycle. It does not infer progress from tools or issue model requests.
func (s *slackStream) Lifecycle(event transcript.Event) {
	if event.Type == transcript.DelegatedTaskCompleted {
		s.presentDelegatedResult(event)
		return
	}
	if event.Type == transcript.PlanUpdated {
		s.UpdatePlan(event.Plan)
		return
	}
	if event.Type == transcript.ApprovalRequested {
		s.requestApproval(event)
		return
	}
	if s.session == nil {
		return
	}
	switch event.Type {
	case transcript.TurnCompleted:
		s.setSessionStatus(sessionStatusForTermination(event.Status))
	case transcript.TurnFailed, transcript.TurnCanceled:
		s.setSessionStatus(sessionActive)
	}
}

func (s *slackStream) presentDelegatedResult(event transcript.Event) {
	if event.Message == nil {
		return
	}
	var metadata struct {
		ChildRun struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"child_run"`
	}
	if json.Unmarshal(event.Metadata, &metadata) != nil {
		return
	}
	role := strings.TrimSpace(metadata.ChildRun.Role)
	name := strings.TrimSpace(metadata.ChildRun.Name)
	if role == "" {
		role = "Review worker"
	}
	if name == "" {
		name = role
	}
	text := strings.TrimSpace(event.Message.Text())
	if text == "" {
		return
	}
	const maxRunes = 2400
	if runes := []rune(text); len(runes) > maxRunes {
		text = string(runes[:maxRunes]) + "…"
	}
	message := "*Candidate report — awaiting Lead verification*\n" + text
	persona := slackconversation.Persona{Name: "Kepler · " + name, IconEmoji: personaEmoji(role + " " + name)}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if messenger, ok := s.messenger.(slackconversation.AttributedMessenger); ok {
		if _, err := messenger.PostMessageAs(ctx, s.req.Channel, s.req.ThreadTS, message, persona); err == nil {
			return
		} else {
			log.Printf("slack reviewer persona unavailable turn=%s role=%s: %v", event.TurnID, role, err)
		}
	}
	_, _ = s.messenger.PostMessage(ctx, s.req.Channel, s.req.ThreadTS, "*["+name+"] Candidate report — awaiting Lead verification*\n"+text)
}

func personaEmoji(identity string) string {
	identity = strings.ToLower(identity)
	switch {
	case strings.Contains(identity, "verif"), strings.Contains(identity, "验证"):
		return ":mag:"
	case strings.Contains(identity, "secur"), strings.Contains(identity, "安全"):
		return ":shield:"
	case strings.Contains(identity, "test"), strings.Contains(identity, "测试"):
		return ":test_tube:"
	case strings.Contains(identity, "data"), strings.Contains(identity, "数据库"):
		return ":floppy_disk:"
	default:
		return ":robot_face:"
	}
}

func (s *slackStream) requestApproval(event transcript.Event) {
	if event.ToolCall == nil {
		return
	}
	messenger, ok := s.messenger.(slackconversation.ApprovalMessenger)
	if !ok {
		return
	}
	call := event.ToolCall
	value, err := json.Marshal(map[string]string{"turn_id": event.TurnID, "tool_call_id": call.ID})
	if err != nil {
		return
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = "this action"
	}
	description := approvalDescription(name, call.Arguments)
	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*Confirmation required*\nApprove this action?\n" + description}},
		{"type": "actions", "elements": []map[string]any{
			{"type": "button", "action_id": "agent_approval_approve", "style": "primary", "text": map[string]any{"type": "plain_text", "text": "Confirm"}, "value": string(value)},
			{"type": "button", "action_id": "agent_approval_decline", "style": "danger", "text": map[string]any{"type": "plain_text", "text": "Cancel"}, "value": string(value)},
		}},
	}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if _, err := messenger.PostMessageBlocks(ctx, s.req.Channel, s.req.ThreadTS, "Confirmation required for "+name, blocks); err != nil {
		log.Printf("slack approval prompt failed turn=%s call=%s: %v", event.TurnID, call.ID, err)
	}
}

func approvalDescription(name string, arguments json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "this action"
	}
	var formatted bytes.Buffer
	if len(arguments) > 0 && json.Indent(&formatted, arguments, "", "  ") == nil {
		text := string(formatted.Bytes())
		if len([]rune(text)) > 1_500 {
			text = string([]rune(text)[:1_500]) + "…"
		}
		return name + "\n```" + text + "```"
	}
	return name
}

func sessionStatusForTermination(termination string) string {
	switch termination {
	case "pending_input", "pending_approval":
		return sessionSuspended
	default:
		return sessionActive
	}
}

func (s *slackStream) setSessionStatus(status string) {
	if s.session == nil || status == "" {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionStatus == status {
		return
	}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if err := s.session.SetAgentSessionStatus(ctx, s.req.Channel, s.req.ThreadTS, s.req.UserID, status); err != nil {
		log.Printf("slack agent session status unavailable turn=%s status=%s", s.req.EventID, status)
		return
	}
	s.sessionStatus = status
}
