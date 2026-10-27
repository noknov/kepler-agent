package slacktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
	slack "github.com/noknov/kepler-agent/packages/surfaces/slack/client"
	github "github.com/noknov/kepler-agent/packages/tools/github"
)

// WorkObjectSource owns resource authorization and canonical state. Tool arguments
// select a resource; they cannot supply a title, status, revision or progress.
type WorkObjectSource interface {
	Snapshot(context.Context, tool.Call, string) (slack.WorkObject, error)
}

type GitHubWorkObjectSource struct{ Source github.ClientSource }

func (s GitHubWorkObjectSource) Snapshot(ctx context.Context, call tool.Call, link string) (slack.WorkObject, error) {
	if s.Source == nil {
		return slack.WorkObject{}, fmt.Errorf("GitHub task source is not configured")
	}
	client, err := s.Source.Resolve(ctx, call)
	if err != nil {
		return slack.WorkObject{}, err
	}
	task, err := client.ReadTaskSnapshot(ctx, link)
	if err != nil {
		return slack.WorkObject{}, err
	}
	payload := slack.WorkObjectPayload{
		Attributes: slack.WorkObjectAttributes{Title: slack.WorkObjectTitle{Text: task.Title}, DisplayID: fmt.Sprintf("%s#%d", task.Repository, task.Number), MetadataLastModified: task.UpdatedAt.Unix()},
		Fields:     map[string]slack.WorkObjectField{"status": {Value: task.State}, "date_updated": {Value: task.UpdatedAt.Unix()}},
	}
	if task.HeadSHA != "" {
		payload.CustomFields = []slack.WorkObjectCustomField{{Key: "head_revision", Label: "PR head", Value: task.HeadSHA, Type: "string"}}
	}
	return slack.WorkObject{AppUnfurlURL: link, URL: task.URL, ExternalRef: slack.WorkObjectRef{ID: task.ID, Type: task.Kind}, EntityType: "slack#/entities/task", EntityPayload: payload}, nil
}

type UserUnfurlTaskTool struct {
	Slack ClientSource
	Tasks WorkObjectSource
}

func (UserUnfurlTaskTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor("slack-user_unfurl_task", "On explicit request, refresh one Work Object preview for a GitHub issue/PR link already posted in a known Slack message. Both integrations use the requesting user's linked accounts. State comes from GitHub, never model-authored progress. Requires the app to have the source domain registered for link unfurling; no automatic investigation or editable fields are installed.", tool.ObjectSchema([]string{"url", "channel", "message_ts"}, map[string]any{
		"url": map[string]any{"type": "string", "description": "Exact GitHub issue/PR URL already present in the target message."}, "channel": map[string]any{"type": "string", "minLength": 1}, "message_ts": map[string]any{"type": "string", "minLength": 1},
	}), externalWrite("slack-connection", "github-connection")...)
}
func (t UserUnfurlTaskTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var in struct {
		URL       string `json:"url"`
		Channel   string `json:"channel"`
		MessageTS string `json:"message_ts"`
	}
	if err := json.Unmarshal(call.Arguments, &in); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(in.URL) == "" || strings.TrimSpace(in.Channel) == "" || strings.TrimSpace(in.MessageTS) == "" {
		return tool.Result{}, fmt.Errorf("url, channel and message_ts are required; no destination is inferred")
	}
	if t.Tasks == nil {
		return tool.Result{}, fmt.Errorf("task source is not configured")
	}
	client, early, err := beginClientAccess(ctx, t.Slack, call)
	if early != nil {
		return *early, err
	}
	if err != nil {
		return tool.Result{}, err
	}
	object, err := t.Tasks.Snapshot(ctx, call, in.URL)
	if err != nil {
		if result, convErr := connections.ToolResult(err); convErr == nil {
			return result, nil
		}
		return tool.Result{}, err
	}
	if err := client.UnfurlWorkObject(ctx, in.Channel, in.MessageTS, object); err != nil {
		return slackMutationFailure(err)
	}
	return structuredSlackResult(map[string]any{"channel": in.Channel, "message_ts": in.MessageTS, "object": object})
}
