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
//
// The update is only delivered if the parent ctx is still live when the LLM
// responds. This prevents stale status text from appearing after the run ends.
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
		if err != nil {
			return
		}
		// Double-check the parent context after the LLM call returns.
		// The timeout context (sctx) may have succeeded just as the parent was
		// cancelled, so we must check both to avoid a TOCTOU race where the run
		// has ended but we still deliver a status update.
		select {
		case <-ctx.Done():
			return
		default:
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
			"用不超过10个字写一条操作状态，格式像进程日志或系统监控（例：读取配置文件、搜索提交记录、查询 Pod 日志）。"+
				"只输出动作本身，不加标点，不加主语。\n工具：%s\n参数：%s",
			names, sampleArgs,
		)
	}
	return fmt.Sprintf(
		"Write a ≤10-word operation status, like a system process log entry (e.g. \"Reading config\", \"Fetching pod logs\", \"Searching commit history\"). "+
			"Output only the action, no punctuation, no subject.\nTools: %s\nArgs: %s",
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
