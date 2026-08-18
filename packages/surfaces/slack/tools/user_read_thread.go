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

// UserReadThreadTool reads a Slack thread using the requesting user's connected identity.
// Registered only from slacktool.AddToCatalog in the Slack worker; never from CLI.
type UserReadThreadTool struct {
	Source ThreadReaderSource
}

func (t UserReadThreadTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"slack-user_read_thread",
		"Read Slack messages as the requesting user's connected Slack identity. Use for DMs, channels, or threads the user can access. Defaults to the current conversation; for private-message tabs this loads prior DM history, and for threaded replies it loads the thread.",
		tool.ObjectSchema([]string{}, map[string]any{
			"channel": map[string]any{
				"type":        "string",
				"description": "Slack channel ID (C...) or DM ID (D...). Defaults to the current conversation channel.",
			},
			"thread_ts": map[string]any{
				"type":        "string",
				"description": "Parent message timestamp for the thread. Defaults to the current conversation thread.",
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
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.Channel = strings.TrimSpace(args.Channel)
	args.ThreadTS = strings.TrimSpace(args.ThreadTS)
	if args.Channel == "" {
		args.Channel = strings.TrimSpace(call.Scope.Values["channel"])
	}
	if args.ThreadTS == "" {
		args.ThreadTS = strings.TrimSpace(call.Scope.Values["thread_ts"])
	}
	messageTS := strings.TrimSpace(call.Scope.Values["message_ts"])
	if args.Channel == "" {
		return tool.Result{}, fmt.Errorf("channel is required")
	}
	if args.Limit <= 0 {
		args.Limit = defaultThreadLimit
	}
	if args.Limit > maxThreadLimit {
		args.Limit = maxThreadLimit
	}
	var messages []slack.Message
	if slack.UseConversationHistory(args.ThreadTS, messageTS) {
		latest := messageTS
		if latest == "" {
			latest = args.ThreadTS
		}
		messages, err = reader.History(ctx, args.Channel, latest, args.Limit)
	} else {
		if args.ThreadTS == "" {
			return tool.Result{}, fmt.Errorf("thread_ts is required for threaded reads")
		}
		messages, err = reader.Replies(ctx, args.Channel, args.ThreadTS, args.Limit)
	}
	if err != nil {
		return tool.Result{}, err
	}
	if len(messages) == 0 {
		return tool.TextResult(fmt.Sprintf("No messages found in conversation %s.", args.Channel)), nil
	}
	return tool.TextResult(formatThreadMessages(args.Channel, displayThreadTS(args.ThreadTS, messageTS), messages)), nil
}

func displayThreadTS(threadTS, messageTS string) string {
	if threadTS != "" {
		return threadTS
	}
	return messageTS
}

func formatThreadMessages(channel, threadTS string, messages []slack.Message) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Slack thread\nchannel: %s\nthread_ts: %s\n", channel, threadTS))
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
