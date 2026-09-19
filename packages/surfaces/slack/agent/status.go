package slackagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/connections"
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
	if event.Type == transcript.PlanUpdated {
		s.UpdatePlan(event.Plan)
		return
	}
	if event.Type == transcript.ApprovalRequested {
		s.requestApproval(event)
		return
	}
	if event.Type == transcript.ToolCallFailed && event.ToolResult != nil && event.ToolResult.ErrorCode == "connection_required" {
		s.requestConnection(event)
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

func (s *slackStream) requestConnection(event transcript.Event) {
	if event.ToolResult == nil || event.ToolCall == nil {
		return
	}
	action, ok := event.ToolResult.Metadata["connection_action"].(connections.ConnectionAction)
	if !ok {
		if pointer, pointerOK := event.ToolResult.Metadata["connection_action"].(*connections.ConnectionAction); pointerOK && pointer != nil {
			action, ok = *pointer, true
		}
	}
	if !ok {
		// JSON transcript/replay paths may decode metadata as map[string]any.
		raw, marshalErr := json.Marshal(event.ToolResult.Metadata["connection_action"])
		if marshalErr != nil || json.Unmarshal(raw, &action) != nil {
			return
		}
	}
	if action.Provider == "" {
		return
	}
	messenger, ok := s.messenger.(slackconversation.ConnectionActionMessenger)
	if !ok {
		return
	}
	key := event.TurnID + ":" + event.ToolCall.ID
	s.mu.Lock()
	if s.connectionActions == nil {
		s.connectionActions = make(map[string]bool)
	}
	if s.connectionActions[key] {
		s.mu.Unlock()
		return
	}
	s.connectionActions[key] = true
	s.mu.Unlock()
	title := action.Label
	if title == "" {
		title = action.Provider
	}
	verb := "Connect"
	if action.State == "reauthorize" {
		verb = "Reconnect"
	}
	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": fmt.Sprintf("*%s connection required*\n%s needs access before I can continue.", verb, title)}},
	}
	button := map[string]any{
		"type":      "button",
		"action_id": "connection_open",
		"text":      map[string]any{"type": "plain_text", "text": verb, "emoji": true},
		"style":     "primary",
	}
	connectURL := strings.TrimSpace(action.URL)
	if s.connections != nil {
		contextualURL, err := s.connections.ConnectURLForInstanceWithContext(
			s.req.UserID,
			action.Provider,
			action.InstanceID,
			action.Label,
			action.Metadata,
			connections.ConnectionContext{Origin: "chat", ReturnContext: slackConnectionReturnContext(s.req.Channel, s.req.ThreadTS)},
		)
		if err == nil {
			connectURL = contextualURL
		}
	}
	if connectURL != "" && action.State != "configure" {
		button["url"] = connectURL
	} else {
		// No OAuth URL is available yet. Let the user enter the provider's
		// declared connection fields through the same Add integration flow.
		button["action_id"] = "add_connection"
		button["value"] = connectionActionValue(action.Provider, "chat", slackConnectionReturnContext(s.req.Channel, s.req.ThreadTS))
		button["text"] = map[string]any{"type": "plain_text", "text": "Add", "emoji": true}
	}
	blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{button}})
	ctx, cancel := s.deliveryContext()
	defer cancel()
	if _, err := messenger.PostMessageBlocks(ctx, s.req.Channel, s.req.ThreadTS, title+" connection required", blocks); err != nil {
		log.Printf("slack connection action failed provider=%s instance=%s: %v", action.Provider, action.InstanceID, err)
	}
}

func slackConnectionReturnContext(channel, threadTS string) string {
	if strings.TrimSpace(channel) == "" && strings.TrimSpace(threadTS) == "" {
		return ""
	}
	raw, err := json.Marshal(map[string]string{"channel": channel, "thread_ts": threadTS})
	if err != nil {
		return ""
	}
	return string(raw)
}

func connectionActionValue(provider, origin, returnContext string) string {
	raw, err := json.Marshal(map[string]string{"provider": provider, "origin": origin, "return_context": returnContext})
	if err != nil {
		return provider
	}
	return string(raw)
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

func approvalResolutionBlocks(approved bool) []map[string]any {
	text := "◼ Canceled — this action was not run."
	if approved {
		text = "✅ Confirmed — action completed or is continuing."
	}
	return []map[string]any{{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": text}}}}
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
