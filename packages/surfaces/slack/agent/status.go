package slackagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
		if !s.exposesWorkerResults() {
			return
		}
		s.deliverDelegatedResult(event)
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

func (s *slackStream) SetExposeWorkerResults(expose bool) {
	s.delegatedMu.Lock()
	s.exposeWorkerResults = expose
	s.delegatedMu.Unlock()
}

func (s *slackStream) exposesWorkerResults() bool {
	s.delegatedMu.Lock()
	defer s.delegatedMu.Unlock()
	return s.exposeWorkerResults
}

// ReplayDelegatedResults rebuilds the non-authoritative Slack worker-message
// projection from the canonical parent transcript.
func (s *slackStream) ReplayDelegatedResults(turnID string, events []transcript.Event) {
	if !s.exposesWorkerResults() {
		return
	}
	for _, event := range events {
		if event.TurnID == turnID && event.Type == transcript.DelegatedTaskCompleted {
			s.deliverDelegatedResult(event)
		}
	}
}

func (s *slackStream) deliverDelegatedResult(event transcript.Event) {
	key := event.ID
	if key == "" {
		key = event.TurnID + ":" + string(event.Metadata)
	}
	s.delegatedMu.Lock()
	if s.delegatedDelivered[key] || s.delegatedInFlight[key] {
		s.delegatedMu.Unlock()
		return
	}
	s.delegatedInFlight[key] = true
	s.delegatedMu.Unlock()

	err := s.presentDelegatedResult(event)
	s.delegatedMu.Lock()
	delete(s.delegatedInFlight, key)
	if err == nil {
		s.delegatedDelivered[key] = true
	}
	s.delegatedMu.Unlock()
	if err != nil {
		log.Printf("slack reviewer report delivery failed turn=%s event=%s error_code=%s error_type=%T", event.TurnID, event.ID, safeSlackErrorCode(err), err)
	}
}

func (s *slackStream) presentDelegatedResult(event transcript.Event) error {
	if event.Message == nil {
		return nil
	}
	var metadata struct {
		ChildRun struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"child_run"`
	}
	if json.Unmarshal(event.Metadata, &metadata) != nil {
		return nil
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
		return nil
	}
	message := "> Candidate evidence; the Lead will verify and synthesize it.\n\n" + text
	persona := slackconversation.Persona{Name: name, IconEmoji: personaEmoji(name)}
	ctx, cancel := s.deliveryContext()
	defer cancel()
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if messenger, ok := s.messenger.(slackconversation.AttributedMarkdownMessenger); ok {
		if _, err := messenger.PostMarkdownMessageAs(ctx, s.req.Channel, s.req.ThreadTS, message, persona, event.ID); err == nil {
			return nil
		} else {
			log.Printf("slack reviewer persona markdown unavailable turn=%s role=%s error_code=%s error_type=%T", event.TurnID, role, safeSlackErrorCode(err), err)
		}
	} else if messenger, ok := s.messenger.(slackconversation.AttributedMessenger); ok {
		if _, err := messenger.PostMessageAs(ctx, s.req.Channel, s.req.ThreadTS, message, persona); err == nil {
			return nil
		} else {
			log.Printf("slack reviewer persona unavailable turn=%s role=%s error_code=%s error_type=%T", event.TurnID, role, safeSlackErrorCode(err), err)
		}
	}
	if messenger, ok := s.messenger.(slackconversation.IdempotentMarkdownMessenger); ok {
		_, err := messenger.PostMarkdownMessageWithID(ctx, s.req.Channel, s.req.ThreadTS, "## "+name+"\n\n"+message, event.ID)
		return err
	}
	_, err := s.messenger.PostMarkdownMessage(ctx, s.req.Channel, s.req.ThreadTS, "## "+name+"\n\n"+message)
	return err
}

func personaEmoji(identity string) string {
	palette := [...]string{":robot_face:", ":mag:", ":shield:", ":test_tube:", ":floppy_disk:"}
	digest := sha256.Sum256([]byte(strings.TrimSpace(identity)))
	return palette[int(digest[0])%len(palette)]
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
		log.Printf("slack agent session status unavailable turn=%s status=%s error_code=%s error_type=%T", s.req.EventID, status, safeSlackErrorCode(err), err)
		return
	}
	s.sessionStatus = status
}

func safeSlackErrorCode(err error) string {
	var coded interface{ SlackErrorCode() string }
	if errors.As(err, &coded) && strings.TrimSpace(coded.SlackErrorCode()) != "" {
		return strings.TrimSpace(coded.SlackErrorCode())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "unknown"
}
