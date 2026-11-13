package taskspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

type Client interface {
	CodeChannelInfo(context.Context, string) (slack.CodeChannelInfo, error)
	CreateCodeChannel(context.Context, slack.CodeChannelCreate) (string, error)
	SetCodeContext(context.Context, string, []slack.CodeContextItem) error
	SetCodeView(context.Context, slack.CodeView) error
	PostMarkdownMessageWithID(context.Context, string, string, string, string) (string, error)
}

type Service struct {
	Store     Store
	Slack     Client
	Access    safety.AccessPolicy
	BotUserID string
}

func (s Service) allowedOrigin(user, channel string) bool {
	return user != "" && channel != "" && s.Access.AllowsUser(user) && (strings.HasPrefix(channel, "D") || s.Access.AllowsChannel(channel))
}

// Resolve fails closed before metadata access. A saved grant permits its owner
// only while the original conversation remains authorized. Channel invites do
// not grant Kepler access to an origin's private history.
func (s Service) Resolve(ctx context.Context, team, user, channel string) (conversation.Target, bool, error) {
	if team == "" || user == "" || channel == "" || !s.Access.AllowsUser(user) {
		return conversation.Target{}, false, nil
	}
	target := conversation.Target{Kind: conversation.CodeChannelSession, Channel: channel}
	allowed := s.Access.AllowsChannel(channel)
	if s.Store != nil {
		grant, err := s.Store.Get(ctx, team, channel)
		switch {
		case err == nil:
			if grant.OwnerID != user || !s.allowedOrigin(user, grant.OriginChannel) {
				return target, false, nil
			}
			allowed = true
		case !errors.Is(err, ErrNotFound):
			return target, false, err
		}
	}
	if !allowed {
		return conversation.Target{}, false, nil
	}
	info, err := s.Slack.CodeChannelInfo(ctx, channel)
	if err != nil {
		return conversation.Target{}, false, err
	}
	if !info.IsCodeChannel() {
		return conversation.Target{}, false, nil
	}
	if info.IsArchived || !info.HasAgent(s.BotUserID) {
		return target, false, nil
	}
	return target, true, nil
}

func (s Service) Create(ctx context.Context, scope tool.Scope, name, brief string) (Space, error) {
	if strings.TrimSpace(brief) == "" || utf8.RuneCountInString(brief) > 4000 {
		return Space{}, fmt.Errorf("task brief must contain one to 4000 characters")
	}
	team, channel, ts := scope.Values["team_id"], scope.Values["channel"], scope.Values["message_ts"]
	if scope.Values["surface"] != "slack" || team == "" || ts == "" || !s.allowedOrigin(scope.UserID, channel) {
		return Space{}, fmt.Errorf("task space requires an authorized Slack origin")
	}
	if scope.Values["session_kind"] == string(conversation.CodeChannelSession) {
		return Space{}, fmt.Errorf("continue the current task space instead of creating a nested space")
	}
	if s.Store == nil || s.Slack == nil {
		return Space{}, fmt.Errorf("task spaces are unavailable")
	}
	// Quote each identity component to make tuple boundaries unambiguous.
	identity := fmt.Sprintf("%q/%q/%q/%q", team, scope.UserID, channel, ts)
	request := slack.CodeChannelCreate{TeamID: team, SessionID: fmt.Sprintf("kepler-space-%x", sha256.Sum256([]byte(identity))), OriginChannel: channel, OriginMessageTS: ts, Name: name}
	created, err := s.Slack.CreateCodeChannel(ctx, request)
	if err != nil {
		return Space{}, err
	}
	space := Space{TeamID: team, Channel: created, OwnerID: scope.UserID, OriginChannel: channel, OriginMessageTS: ts}
	// A crash before save is repaired by Slack's stable session idempotency key.
	// Until save succeeds, the new channel grants no extra Kepler authorization.
	if err := s.Store.Save(ctx, space); err != nil {
		return Space{}, err
	}
	if _, err := s.Slack.PostMarkdownMessageWithID(ctx, created, "", brief, request.SessionID+":brief"); err != nil {
		return Space{}, err
	}
	return space, nil
}

func (s Service) Current(ctx context.Context, scope tool.Scope) (string, error) {
	if scope.Values["surface"] != "slack" || scope.Values["session_kind"] != string(conversation.CodeChannelSession) {
		return "", fmt.Errorf("this operation requires the current native task space")
	}
	target, allowed, err := s.Resolve(ctx, scope.Values["team_id"], scope.UserID, scope.Values["channel"])
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", fmt.Errorf("task space is not authorized")
	}
	return target.Channel, nil
}
