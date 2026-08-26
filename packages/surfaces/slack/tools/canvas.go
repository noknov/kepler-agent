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
	SetCanvasAccess(ctx context.Context, canvasID string, accessLevel string, channelIDs []string) error
}

type CreateCanvasTool struct {
	Slack CanvasCreator
}

func (t CreateCanvasTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"slack-create_canvas",
		"",
		tool.ObjectSchema([]string{"title", "markdown"}, map[string]any{
			"title":      map[string]any{"type": "string", "description": ""},
			"markdown":   map[string]any{"type": "string", "description": ""},
			"channel_id": map[string]any{"type": "string", "description": ""},
		}),
		externalWrite()...,
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

	channelID := args.ChannelID
	if channelID == "" {
		channelID = call.Scope.Values["channel"]
	}

	if channelID != "" {
		canvasID, err = t.Slack.CreateChannelCanvas(ctx, channelID, args.Title, content)
	} else {
		canvasID, err = t.Slack.CreateCanvas(ctx, args.Title, content)
	}
	if err != nil {
		return tool.Result{}, err
	}

	return tool.TextResult(fmt.Sprintf("Canvas created successfully.\nCanvas ID: %s\nTitle: %s", canvasID, args.Title)), nil
}
