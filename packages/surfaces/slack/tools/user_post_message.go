package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	slackmessaging "github.com/noknov/kepler-agent/packages/surfaces/slack/messaging"
)

// UserPostMessageTool posts to Slack using the requesting user's connected identity.
// Registered only from slacktool.AddToCatalog in the Slack worker; never from CLI.
// It must not be used to reply in the current bot conversation.
type UserPostMessageTool struct {
	Source      ConnectedClientSource
	Attribution slackmessaging.Attribution
}

func (t UserPostMessageTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"slack-user_post_message",
		"Post a Slack message as the requesting user's connected Slack identity. Use only when the user explicitly asks to send a message on their behalf in Slack (another channel, DM, or thread). Do not use to reply in the current bot conversation.",
		tool.ObjectSchema([]string{"channel", "text"}, map[string]any{
			"channel": map[string]any{
				"type":        "string",
				"description": "Slack channel ID (C...), DM channel ID (D...), or user ID (U...) to message.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Message text to post as the connected user.",
			},
			"thread_ts": map[string]any{
				"type":        "string",
				"description": "Optional parent message timestamp to reply in a thread.",
			},
		}),
		externalWrite("slack-connection")...,
	)
}

func (t UserPostMessageTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.Channel = strings.TrimSpace(args.Channel)
	args.Text = strings.TrimSpace(args.Text)
	args.ThreadTS = strings.TrimSpace(args.ThreadTS)
	if args.Channel == "" {
		args.Channel = strings.TrimSpace(call.Scope.Values["channel"])
	}
	if args.ThreadTS == "" {
		args.ThreadTS = strings.TrimSpace(call.Scope.Values["thread_ts"])
	}
	if args.Channel == "" {
		return tool.Result{}, fmt.Errorf("channel is required")
	}
	if args.Text == "" {
		return tool.Result{}, fmt.Errorf("text is required")
	}
	slackClient, early, err := beginConnectedClient(ctx, t.Source, call)
	if early != nil || err != nil {
		if early != nil {
			return *early, err
		}
		return tool.Result{}, err
	}
	ts, err := slackmessaging.PostAsConnectedUser(ctx, slackClient, args.Channel, args.ThreadTS, args.Text, call.ID, t.Attribution)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.TextResult(fmt.Sprintf("Posted message as connected Slack user.\nchannel: %s\nts: %s", args.Channel, ts)), nil
}
