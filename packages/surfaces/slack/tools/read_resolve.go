package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func readRequiresUserConnection(call tool.Call) bool {
	var args struct {
		User, Channel, Link string
	}
	_ = json.Unmarshal(call.Arguments, &args)
	if strings.TrimSpace(args.User) != "" || strings.TrimSpace(args.Link) != "" {
		return true
	}
	channel := slack.NormalizeChannelRef(args.Channel)
	scopeChannel := strings.TrimSpace(call.Scope.Values["channel"])
	return channel != "" && channel != scopeChannel
}

type readResolver interface {
	ThreadReader
	ConversationResolver
}

func resolveReadTarget(ctx context.Context, reader ThreadReader, channel, user, link, threadTS string, scope map[string]string) (slack.ReadTarget, error) {
	resolver, ok := reader.(readResolver)
	if !ok {
		return slack.ReadTarget{}, fmt.Errorf("slack reader cannot resolve conversation references")
	}
	return resolver.ResolveReadTarget(ctx, slack.ReadTargetInput{
		Channel:        channel,
		User:           user,
		Link:           link,
		ThreadTS:       threadTS,
		ScopeChannel:   scope["channel"],
		ScopeThreadTS:  scope["thread_ts"],
		ScopeMessageTS: scope["message_ts"],
	})
}
