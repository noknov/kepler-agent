package slackhandler

import (
	"context"

	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

// Prompt and Stop use the same authenticated conversation authorization. Native
// sessions are verified by Slack metadata, never by missing thread timestamps.
func (h *Handler) eventTarget(ctx context.Context, ev slack.Event) (conversation.Target, bool, error) {
	thread := ev.ConversationThreadTS()
	if ev.Type == "agent_session_stopped" {
		thread = ev.ThreadTS
	}
	target := conversation.Target{Channel: ev.Channel, ThreadTS: thread}
	if ev.User == "" || ev.Channel == "" || !h.Access.AllowsUser(ev.User) {
		return target, false, nil
	}
	if IsAppDM(ev) {
		return target, target.Validate() == nil, nil
	}
	if h.Spaces != nil {
		native, allowed, err := h.Spaces.Resolve(ctx, ev.TeamID, ev.User, ev.Channel)
		if err != nil {
			return target, false, err
		}
		if native.Kind == conversation.CodeChannelSession {
			return native, allowed, nil
		}
	}
	return target, h.Access.AllowsChannel(ev.Channel) && target.Validate() == nil, nil
}

// Durable acceptance is not permanent authorization. Recheck the current
// installation policy when a claimed input resumes after a worker restart.
func (h *Handler) AuthorizeInput(ctx context.Context, req conversation.Request) (bool, error) {
	target, allowed, err := h.eventTarget(ctx, slack.Event{Type: "app_mention", TeamID: req.TeamID, User: req.UserID, Channel: req.Channel, ThreadTS: req.ThreadTS, TS: req.MessageTS})
	return allowed && target == req.Target(), err
}
