package slacktool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

type CanvasCreator interface {
	CreateCanvas(ctx context.Context, title string, content *slack.CanvasContent) (string, error)
	CreateChannelCanvas(ctx context.Context, channelID, title string, content *slack.CanvasContent) (string, error)
	SetCanvasAccess(ctx context.Context, canvasID string, accessLevel string, channelIDs []string) error
}

type CreateCanvasTool struct {
	Slack CanvasCreator
}

func (CreateCanvasTool) IsWrite() bool { return true }

func (t CreateCanvasTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"slack-create_canvas",
		"",
		registry.ObjectSchema([]string{"title", "markdown"}, map[string]any{
			"title":      map[string]any{"type": "string", "description": ""},
			"markdown":   map[string]any{"type": "string", "description": ""},
			"channel_id": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t CreateCanvasTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Title     string `json:"title"`
		Markdown  string `json:"markdown"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Title == "" {
		return registry.Result{}, fmt.Errorf("title is required")
	}
	if args.Markdown == "" {
		return registry.Result{}, fmt.Errorf("markdown content is required")
	}

	content := &slack.CanvasContent{
		Type:     "markdown",
		Markdown: args.Markdown,
	}

	var canvasID string
	var err error

	channelID := args.ChannelID
	if channelID == "" {
		channelID = rt.Channel
	}

	if channelID != "" {
		canvasID, err = t.Slack.CreateChannelCanvas(ctx, channelID, args.Title, content)
	} else {
		canvasID, err = t.Slack.CreateCanvas(ctx, args.Title, content)
	}
	if err != nil {
		return registry.Result{}, err
	}

	return registry.Result{Content: fmt.Sprintf("Canvas created successfully.\nCanvas ID: %s\nTitle: %s", canvasID, args.Title)}, nil
}
