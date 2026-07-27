package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/llm"
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
	if s == nil || s.Client == nil || update == nil {
		return
	}
	go func() {
		timeout := s.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		sctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var text string
		for attempt := 0; attempt < 2; attempt++ {
			resp, err := s.Client.Chat(sctx, llm.Request{
				Model:     s.Model,
				Messages:  []llm.Message{{Role: "user", Content: summarizePrompt(names, sampleArgs, locale)}},
				MaxTokens: 64,
				Thinking:  "disabled",
			})
			if err != nil {
				if attempt == 0 && llm.IsTemporaryOverload(err) {
					continue
				}
				return
			}
			text = strings.TrimSpace(resp.Message.Content)
			if text != "" {
				break
			}
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
		if text != "" {
			update(text)
		}
	}()
}

func summarizePrompt(names, sampleArgs, locale string) string {
	sampleArgs = sanitizeArgs(sampleArgs)
	if locale == LocaleZH {
		return fmt.Sprintf(
			"用不超过20个字写一条操作状态，说清正在做什么和针对什么（从参数提取服务名、文件名、页面等具体对象）。"+
				"格式像进程日志（例：读取告警处理文档、查询 payment 服务日志、搜索最近部署记录）。"+
				"只输出动作本身，不加标点，不加主语。"+
				"禁止输出工具名、函数名、API 路径、内部标识符（如带 -、_、. 的技术名称），用通俗描述代替。\n"+
				"工具：%s\n参数：%s",
			names, sampleArgs,
		)
	}
	return fmt.Sprintf(
		"Write a ≤20-word operation status that explains what is happening and the specific target (service, file, page, etc. from args). "+
			"Format like a process log (e.g. \"Reading alert runbook\", \"Fetching payment service logs\", \"Searching recent deploys\"). "+
			"Output only the action, no punctuation, no subject. "+
			"Never include tool names, function names, API paths, or internal identifiers (names with -, _, .) in the output; use plain descriptions instead.\n"+
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
