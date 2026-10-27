package slacktool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/client"
)

type CanvasCreator interface {
	CreateCanvas(ctx context.Context, title string, content *slack.CanvasContent) (string, error)
	CreateChannelCanvas(ctx context.Context, channelID, title string, content *slack.CanvasContent) (string, error)
}

type CreateCanvasTool struct {
	Slack  CanvasCreator
	Source ClientSource
}

func (t CreateCanvasTool) Descriptor() tool.Descriptor {
	deps := []string{}
	if t.Source != nil {
		deps = append(deps, "slack-connection")
	}
	return tool.FunctionDescriptor(
		"slack-create_canvas",
		"Create a Canvas only when the user explicitly requests publication. Omit channel_id for a standalone Canvas; provide an explicit channel_id only for a channel Canvas. Never infer a publication audience from the current conversation. Uses the connected user when configured; an uncertain creation must not be retried.",
		tool.ObjectSchema([]string{"title", "markdown"}, map[string]any{
			"title":      map[string]any{"type": "string", "description": ""},
			"markdown":   map[string]any{"type": "string", "description": ""},
			"channel_id": map[string]any{"type": "string", "description": ""},
		}),
		externalWrite(deps...)...,
	)
}

func (t CreateCanvasTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Title     string `json:"title"`
		Markdown  string `json:"markdown"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Title == "" {
		return tool.Result{}, fmt.Errorf("title is required")
	}
	if args.Markdown == "" {
		return tool.Result{}, fmt.Errorf("markdown content is required")
	}

	content := &slack.CanvasContent{
		Type:     "markdown",
		Markdown: args.Markdown,
	}

	var canvasID string
	var err error

	creator := t.Slack
	if t.Source != nil {
		client, early, accessErr := beginClientAccess(ctx, t.Source, call)
		if early != nil {
			return *early, accessErr
		}
		if accessErr != nil {
			return tool.Result{}, accessErr
		}
		creator = client
	}
	if creator == nil {
		return tool.Result{}, fmt.Errorf("Slack Canvas client is not configured")
	}
	channelID := args.ChannelID

	if channelID != "" {
		canvasID, err = creator.CreateChannelCanvas(ctx, channelID, args.Title, content)
	} else {
		canvasID, err = creator.CreateCanvas(ctx, args.Title, content)
	}
	if err != nil {
		return slackMutationFailure(err)
	}
	return structuredSlackResult(map[string]any{"canvas_id": canvasID, "title": args.Title, "channel_id": channelID})
}
