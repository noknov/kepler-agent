package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type UserMessagePermalinkTool struct{ Source ClientSource }

func (UserMessagePermalinkTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-user_message_permalink", "Resolve a canonical Slack permalink for one selected evidence message as the connected user. Use its known channel and timestamp after reading/searching; do not enumerate links for all history messages.", tool.ObjectSchema([]string{"channel", "message_ts"}, map[string]any{"channel": map[string]any{"type": "string", "minLength": 1}, "message_ts": map[string]any{"type": "string", "minLength": 1}}), readNetwork("slack-connection")...)
}
func (t UserMessagePermalinkTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var in struct {
		Channel   string `json:"channel"`
		MessageTS string `json:"message_ts"`
	}
	if err := json.Unmarshal(call.Arguments, &in); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(in.Channel) == "" || strings.TrimSpace(in.MessageTS) == "" {
		return tool.Result{}, fmt.Errorf("channel and message_ts are required")
	}
	client, early, err := beginClientAccess(ctx, t.Source, call)
	if early != nil {
		return *early, err
	}
	if err != nil {
		return tool.Result{}, err
	}
	link, err := client.MessagePermalink(ctx, in.Channel, in.MessageTS)
	if err != nil {
		return tool.Result{}, err
	}
	return structuredSlackResult(map[string]string{"channel": in.Channel, "message_ts": in.MessageTS, "permalink": link})
}
