package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	slackfiles "github.com/noknov/kepler-agent/packages/surfaces/slack/files"
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
	slackClient, early, err := beginThreadRead(ctx, t.Source, call)
	if early != nil || err != nil {
		if early != nil {
			return *early, err
		}
		return tool.Result{}, err
	}
	var args struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultThreadLimit
	}
	if limit > maxThreadLimit {
		limit = maxThreadLimit
	}
	target, err := slackClient.ResolveReadTarget(ctx, readTargetInput(call))
	if err != nil {
		return tool.Result{}, err
	}
	messages, err := slackClient.ReadConversation(ctx, target, limit)
	if err != nil {
		return tool.Result{}, err
	}
	if len(messages) == 0 {
		return tool.TextResult(fmt.Sprintf("No messages found in conversation %s.", target.Channel)), nil
	}
	label := target.ThreadTS
	if label == "" {
		label = target.LatestTS
	}
	text := formatConversation(target.Channel, label, messages)
	budget := slackfiles.ThreadImageBudget()
	history := make([]model.Message, 0, len(messages))
	for _, msg := range messages {
		if historyMsg, ok := slackfiles.HistoryMessage(ctx, slackClient, msg, slackClient.BotUserID(), budget); ok {
			history = append(history, historyMsg)
		}
	}
	content := []model.Content{{Type: model.ContentText, Text: text}}
	content = append(content, model.CollectImages(history...)...)
	return tool.Result{Content: content}, nil
}

func formatConversation(channel, anchorTS string, messages []slack.Message) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Slack conversation\nchannel: %s\nanchor_ts: %s\n", channel, anchorTS))
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
