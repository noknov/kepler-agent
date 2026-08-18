package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

const (
	defaultThreadLimit = 50
	maxThreadLimit     = 200
)

// UserReadThreadTool reads Slack messages using the requesting user's connected identity.
// Registered only from slacktool.AddToCatalog in the Slack worker; never from CLI.
type UserReadThreadTool struct {
	Source ThreadReaderSource
}

func (t UserReadThreadTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"slack-user_read_thread",
		"Read Slack messages the requesting user can access. Accepts a Slack user (@mention, user ID, or display name), DM/channel ID, or Slack message link. Omit everything to read the current bot conversation. For a person's recent DM, pass user (for example Stella Zhang) without thread_ts.",
		tool.ObjectSchema([]string{}, map[string]any{
			"user": map[string]any{
				"type":        "string",
				"description": "Slack user ID (U...), @mention, or display/real name to read a direct-message history with that person.",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Slack channel ID (C.../D...) or user ID (U...) to open as a DM. Defaults to the current conversation.",
			},
			"link": map[string]any{
				"type":        "string",
				"description": "Slack message or thread URL. Channel and thread_ts are parsed automatically.",
			},
			"thread_ts": map[string]any{
				"type":        "string",
				"description": "Parent message timestamp for a threaded read. Omit to fetch recent non-threaded conversation history.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of messages to return (default 50, max 200).",
			},
		}),
		readNetwork("slack-connection")...,
	)
}

func (t UserReadThreadTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	reader, early, err := beginThreadRead(ctx, t.Source, call)
	if early != nil || err != nil {
		if early != nil {
			return *early, err
		}
		return tool.Result{}, err
	}
	var args struct {
		User     string `json:"user"`
		Channel  string `json:"channel"`
		Link     string `json:"link"`
		ThreadTS string `json:"thread_ts"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	target, err := resolveReadTarget(ctx, reader, strings.TrimSpace(args.Channel), strings.TrimSpace(args.User), strings.TrimSpace(args.Link), strings.TrimSpace(args.ThreadTS), call.Scope.Values)
	if err != nil {
		return tool.Result{}, err
	}
	if args.Limit <= 0 {
		args.Limit = defaultThreadLimit
	}
	if args.Limit > maxThreadLimit {
		args.Limit = maxThreadLimit
	}
	var messages []slack.Message
	if slack.UseConversationHistory(target.ThreadTS, target.LatestTS) {
		latest := target.LatestTS
		if latest == "" {
			latest = target.ThreadTS
		}
		messages, err = reader.History(ctx, target.Channel, latest, args.Limit)
	} else {
		messages, err = reader.Replies(ctx, target.Channel, target.ThreadTS, args.Limit)
	}
	if err != nil {
		return tool.Result{}, err
	}
	if len(messages) == 0 {
		return tool.TextResult(fmt.Sprintf("No messages found in conversation %s.", target.Channel)), nil
	}
	return tool.TextResult(formatThreadMessages(target.Channel, displayThreadTS(target.ThreadTS, target.LatestTS), messages)), nil
}

func displayThreadTS(threadTS, messageTS string) string {
	if threadTS != "" {
		return threadTS
	}
	return messageTS
}

func formatThreadMessages(channel, threadTS string, messages []slack.Message) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Slack conversation\nchannel: %s\nthread_ts: %s\n", channel, threadTS))
	for i, msg := range messages {
		author := strings.TrimSpace(msg.User)
		if author == "" && msg.BotID != "" {
			author = "bot:" + msg.BotID
		}
		if author == "" {
			author = "unknown"
		}
		text := strings.TrimSpace(slack.NormalizeMentions(msg.Text, ""))
		if files := slack.FormatFiles(msg.Files); files != "" {
			if text != "" {
				text += "\n"
			}
			text += files
		}
		if text == "" {
			text = "[no text]"
		}
		b.WriteString(fmt.Sprintf("\n%d. [%s @ %s]\n%s\n", i+1, author, msg.TS, text))
	}
	return strings.TrimSpace(b.String())
}
