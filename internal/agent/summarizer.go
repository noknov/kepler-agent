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
// describing the current agent step (potentially multiple tools) and delivers
// it via update. It is a no-op when s is nil.
func (s *StatusSummarizer) Summarize(ctx context.Context, names, sampleArgs, locale string, update StatusUpdater) {
	if s == nil || update == nil {
		return
	}
	go func() {
		sctx, cancel := context.WithTimeout(ctx, s.Timeout)
		defer cancel()

		resp, err := s.Client.Chat(sctx, llm.Request{
			Model:     s.Model,
			Messages:  []llm.Message{{Role: "user", Content: summarizePrompt(names, sampleArgs, locale)}},
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

func summarizePrompt(names, sampleArgs, locale string) string {
	sampleArgs = redactQueryValues(sampleArgs)
	if len(sampleArgs) > 200 {
		sampleArgs = sampleArgs[:200]
	}
	if locale == LocaleZH {
		return fmt.Sprintf(
			"用不超过10个字描述 AI 助手正在执行的操作。格式为动词加宾语，例如：读取部署配置、查询 GCP 日志、搜索错误代码。"+
				"禁止出现您想、用户想、需要等以用户视角描述意图的说法。只输出描述文字，不加标点不加引号。\n"+
				"工具：%s\n参数上下文：%s",
			names, sampleArgs,
		)
	}
	return fmt.Sprintf(
		"In 10 words or fewer describe what the AI assistant is DOING right now. "+
			"Use action-verb form: \"Reading pod logs\", \"Searching error codes\", \"Fetching GCP metrics\". "+
			"Never write \"You want to\" or \"The user wants to\". Reply ONLY with the description, no punctuation.\n"+
			"Tools: %s\nContext: %s",
		names, sampleArgs,
	)
}

// redactQueryValues replaces the values of common free-text argument keys
// (query, text, message, input, q) with a short placeholder so the summarizer
// LLM sees the tool context without being steered by the user's raw words.
func redactQueryValues(args string) string {
	for _, key := range []string{`"query"`, `"text"`, `"message"`, `"input"`, `"q"`, `"prompt"`} {
		if idx := strings.Index(args, key+`:"`); idx != -1 {
			// JSON shorthand key:"value" — replace the value portion
			args = redactAfterKey(args, key+`:"`)
		} else if idx = strings.Index(args, key+`": "`); idx != -1 {
			args = redactAfterKey(args, key+`": "`)
		}
	}
	return args
}

func redactAfterKey(s, prefix string) string {
	idx := strings.Index(s, prefix)
	if idx == -1 {
		return s
	}
	start := idx + len(prefix)
	end := strings.Index(s[start:], `"`)
	if end == -1 {
		return s
	}
	end += start
	if end-start > 40 {
		return s[:start] + s[start:start+40] + "..." + s[end:]
	}
	return s
}
