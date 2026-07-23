package slacktool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noknov/slack-copilot-agent/internal/llm"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
)

type Poster interface {
	PostMessage(ctx context.Context, channel, threadTS, text string) (string, error)
}

type AskUserTool struct {
	Slack Poster
}

func (t AskUserTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"slack-ask_user",
		"",
		registry.ObjectSchema([]string{"question"}, map[string]any{
			"question": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t AskUserTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if args.Question == "" {
		return registry.Result{}, fmt.Errorf("question is required")
	}
	_ = ctx
	_ = rt
	return registry.Result{Content: args.Question, NeedsUserInput: true, WaitForUser: true}, nil
}
