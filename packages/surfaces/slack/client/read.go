package slack

import (
	"context"
	"fmt"
	"strings"
)

// ReadTargetInput describes a Slack conversation the caller wants to read.
type ReadTargetInput struct {
	Channel, User, Link, ThreadTS               string
	ScopeChannel, ScopeThreadTS, ScopeMessageTS string
}

// ReadTarget is a normalized conversation read request.
type ReadTarget struct {
	Channel  string
	ThreadTS string
	LatestTS string
}

// RequiresUserConnection reports whether the read must use the caller's token.
func (in ReadTargetInput) RequiresUserConnection() bool {
	if trim(in.Link) != "" || trim(in.User) != "" {
		return true
	}
	channel := NormalizeChannelRef(in.Channel)
	return channel != "" && channel != trim(in.ScopeChannel)
}

// UseConversationHistory reports whether Slack history should be loaded with
// conversations.history instead of conversations.replies.
func UseConversationHistory(threadTS, beforeTS string) bool {
	threadTS = trim(threadTS)
	beforeTS = trim(beforeTS)
	if threadTS == "" {
		return true
	}
	return beforeTS != "" && threadTS == beforeTS
}

// ReadConversation fetches messages for a resolved read target.
func (c *Client) ReadConversation(ctx context.Context, target ReadTarget, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	if UseConversationHistory(target.ThreadTS, target.LatestTS) {
		latest := trim(target.LatestTS)
		if latest == "" {
			latest = trim(target.ThreadTS)
		}
		messages, err := c.History(ctx, target.Channel, latest, limit)
		if err != nil {
			return nil, err
		}
		reverseMessages(messages)
		return messages, nil
	}
	if trim(target.ThreadTS) == "" {
		return nil, fmt.Errorf("thread_ts is required for threaded reads")
	}
	messages, err := c.Replies(ctx, target.Channel, target.ThreadTS, limit)
	if err != nil {
		return nil, err
	}
	if latest := trim(target.LatestTS); latest != "" {
		filtered := messages[:0]
		for _, msg := range messages {
			if msg.TS >= latest {
				continue
			}
			filtered = append(filtered, msg)
		}
		messages = filtered
	}
	return messages, nil
}

func reverseMessages(messages []Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}

func trim(value string) string {
	return strings.TrimSpace(value)
}
