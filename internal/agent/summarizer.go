package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
)

// StatusSummarizer calls a secondary LLM to produce a short, dynamic status
// message describing what a specific tool call is doing. It fires
// asynchronously so it never blocks tool execution.
//
// The static ToolHint is shown immediately; the dynamic summary overwrites it
// when (and if) the LLM responds within Timeout.
type StatusSummarizer struct {
	Client  llm.Client
	Model   string
	Timeout time.Duration
}

// Summarize launches a background goroutine that generates a short status line
// and delivers it via update. It is a no-op when s is nil.
func (s *StatusSummarizer) Summarize(ctx context.Context, name, args, locale string, update StatusUpdater) {
	if s == nil || update == nil {
		return
	}
	go func() {
		sctx, cancel := context.WithTimeout(ctx, s.Timeout)
		defer cancel()

		resp, err := s.Client.Chat(sctx, llm.Request{
			Model:     s.Model,
			Messages:  []llm.Message{{Role: "user", Content: summarizePrompt(name, args, locale)}},
			MaxTokens: 32,
		})
		if err != nil || sctx.Err() != nil {
			return
		}
		if text := strings.TrimSpace(resp.Message.Content); text != "" {
			update(text)
		}
	}()
}

func summarizePrompt(name, args, locale string) string {
	if len(args) > 300 {
		args = args[:300]
	}
	if locale == LocaleZH {
		return fmt.Sprintf(
			"用10个字以内描述以下工具调用正在做什么，只回复描述文字，不加标点符号。\n工具：%s\n参数：%s",
			name, args,
		)
	}
	return fmt.Sprintf(
		"In 10 words or fewer, describe what this tool call is doing. Reply ONLY with the description, no punctuation.\nTool: %s\nArgs: %s",
		name, args,
	)
}
