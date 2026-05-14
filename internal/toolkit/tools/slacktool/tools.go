package slacktool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
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
		"Ask the Slack user for missing information, then pause. Use only when you cannot safely proceed without clarification.",
		registry.ObjectSchema([]string{"question"}, map[string]any{
			"question": map[string]any{"type": "string", "description": "Specific question to ask in the current thread."},
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
	if t.Slack != nil {
		_, err := t.Slack.PostMessage(ctx, rt.Channel, rt.ThreadTS, "<@"+rt.UserID+"> "+args.Question)
		if err != nil {
			return registry.Result{}, err
		}
	}
	return registry.Result{Content: "asked user: " + args.Question, WaitForUser: true}, nil
}
