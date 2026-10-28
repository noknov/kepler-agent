package slackhandler

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/session"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
	slackgateway "github.com/noknov/kepler-agent/packages/surfaces/slack/gateway"
)

// ApprovalAuthority resolves controls from the durable runtime, before queue
// admission. A signed Slack interaction alone does not authorize a tool write.
type ApprovalAuthority interface {
	ApprovalScope(context.Context, string, agentruntime.ApprovalResolution) (tool.Scope, error)
}

func (h *Handler) resolveApproval(ctx context.Context, interaction slackgateway.Interaction, actionID, value string) {
	if h.Inputs == nil || h.Approvals == nil || interaction.Channel == "" || !h.Access.AllowsUser(interaction.UserID) {
		return
	}
	var token struct {
		SessionID  string `json:"session_id"`
		TurnID     string `json:"turn_id"`
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal([]byte(value), &token); err != nil || token.SessionID == "" || token.TurnID == "" || token.ToolCallID == "" {
		log.Printf("invalid Slack approval control: %v", err)
		return
	}
	approved := actionID == "agent_approval_approve"
	verb := "decline"
	if approved {
		verb = "approve"
	}
	sessionID := token.SessionID
	scope, err := h.Approvals.ApprovalScope(ctx, sessionID, agentruntime.ApprovalResolution{
		TurnID: token.TurnID, ToolCallID: token.ToolCallID, Approved: approved, UserID: interaction.UserID,
	})
	if err != nil || scope.UserID == "" {
		return
	}
	if scope.Values["team_id"] != interaction.TeamID {
		return
	}
	channel, thread := scope.Values["channel"], scope.Values["thread_ts"]
	if channel == "" || thread == "" || session.ID(channel, thread) != sessionID || (!IsDMChannel(channel) && !h.Access.AllowsChannel(channel)) {
		return
	}
	if interaction.Channel != channel || interaction.ThreadTS != thread {
		return
	}
	request := slackconversation.Request{
		TeamID:           interaction.TeamID,
		ConversationType: scope.Values["conversation_type"],
		EventID:          "approval-" + token.TurnID + "-" + token.ToolCallID + "-" + verb,
		MessageID:        "approval-continuation:" + token.TurnID + ":" + token.ToolCallID,
		UserID:           scope.UserID, Channel: channel, ThreadTS: thread,
		Text:     "The requesting user " + verb + "d the pending action. Continue from the recorded tool result.",
		Approval: &slackconversation.ApprovalRequest{TurnID: token.TurnID, ToolCallID: token.ToolCallID, Approved: approved, MessageTS: interaction.MessageTS},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return
	}
	if h.Slack != nil && interaction.MessageTS != "" {
		if err := h.Slack.UpdateMessageBlocks(ctx, interaction.Channel, interaction.MessageTS, "Approval pending", approvalStateBlocks("pending", "", interaction.UserID)); err != nil {
			log.Printf("mark Slack approval pending failed turn=%s call=%s: %v", token.TurnID, token.ToolCallID, err)
		}
	}
	if err := h.Inputs.Enqueue(ctx, sessioninput.Item{ID: request.EventID, SessionID: sessionID, Kind: sessioninput.KindQueue, Payload: payload}); err != nil {
		log.Printf("resolve Slack approval failed turn=%s call=%s: %v", token.TurnID, token.ToolCallID, err)
		if h.Slack != nil && interaction.MessageTS != "" {
			if updateErr := h.Slack.UpdateMessageBlocks(ctx, interaction.Channel, interaction.MessageTS, "Approval failed — retry available", approvalStateBlocks("failed", value, interaction.UserID)); updateErr != nil {
				log.Printf("mark Slack approval failed state failed turn=%s call=%s: %v", token.TurnID, token.ToolCallID, updateErr)
			}
		}
		return
	}
	if h.NotifyApproval != nil {
		h.NotifyApproval(ctx, sessionID)
	}
}

func approvalStateBlocks(state, retryValue, approver string) []map[string]any {
	text := "⏳ Pending — processing this action."
	if state == "failed" {
		text = "⚠ Could not submit this decision. Use the buttons below to retry safely."
	}
	blocks := []map[string]any{{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": text}}}}
	if state == "failed" && strings.TrimSpace(retryValue) != "" {
		value := retryValue
		blocks = append(blocks, map[string]any{
			"type": "actions",
			"elements": []map[string]any{
				{"type": "button", "action_id": "agent_approval_approve", "style": "primary", "text": plainText("Confirm"), "value": value, "visible_to_user_ids": []string{approver}},
				{"type": "button", "action_id": "agent_approval_decline", "style": "danger", "text": plainText("Cancel"), "value": value, "visible_to_user_ids": []string{approver}},
			},
		})
	}
	return blocks
}
