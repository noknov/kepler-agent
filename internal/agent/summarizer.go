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
	sampleArgs = sanitizeArgs(sampleArgs)
	if locale == LocaleZH {
		return fmt.Sprintf(
			"用不超过10个字描述 AI 助手当前这一步在做什么，只回复描述文字，不加标点符号。"+
				"以动词开头，描述 AI 的动作而非用户的意图，禁止出现「您想」「用户想」「需要」等说法。\n"+
				"工具列表：%s\n参数：%s",
			names, sampleArgs,
		)
	}
	return fmt.Sprintf(
		"In 10 words or fewer, describe what the AI is doing in this step. "+
			"Reply ONLY with the description, no punctuation. "+
			"Start with an action verb. Never write \"You want to\" or \"The user wants\".\n"+
			"Tools: %s\nArgs: %s",
		names, sampleArgs,
	)
}

// sanitizeArgs trims the raw args JSON to a compact, summarizer-friendly form.
// Technical parameters (shell commands, repo paths, namespaces) are kept intact
// because they provide the specific context that makes summaries useful. Only
// long free-text fields that likely contain the user's verbatim question are
// truncated to prevent the summarizer from paraphrasing user intent.
func sanitizeArgs(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return ""
	}
	// Keep shell commands fully intact — they contain the most useful context.
	if strings.Contains(args, `"command"`) {
		if len(args) > 300 {
			return args[:300] + "..."
		}
		return args
	}
	// Truncate long values for free-text query fields only.
	for _, key := range []string{`"query"`, `"text"`, `"message"`, `"input"`, `"prompt"`, `"q"`} {
		args = truncateLongStringValue(args, key)
	}
	if len(args) > 250 {
		return args[:250] + "..."
	}
	return args
}

// truncateLongStringValue shortens the JSON string value for the given key when
// it exceeds 50 characters. Short values (e.g. a service name or namespace) are
// left unchanged because they provide useful context.
func truncateLongStringValue(s, key string) string {
	for _, sep := range []string{key + `": "`, key + `":"`} {
		idx := strings.Index(s, sep)
		if idx == -1 {
			continue
		}
		start := idx + len(sep)
		end := strings.Index(s[start:], `"`)
		if end == -1 {
			continue
		}
		end += start
		if end-start > 50 {
			s = s[:start] + s[start:start+50] + `..."` + s[end+1:]
		}
	}
	return s
}
