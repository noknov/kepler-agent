package slacktool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type Poster interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
}

type AskUserTool struct {
	Slack Poster
}

func (t AskUserTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"slack-ask_user",
		"",
		tool.ObjectSchema([]string{"question"}, map[string]any{
			"question": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t AskUserTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Question == "" {
		return tool.Result{}, fmt.Errorf("question is required")
	}
	_ = ctx
	return tool.Result{Content: tool.TextResult(args.Question).Content, NeedsUserInput: true}, nil
}
