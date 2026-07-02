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
		return fmt.Sprintf(`根据工具和参数，输出一个不超过10个字符的操作描述（含空格和英文标识符）。
动词要短（查/搜/读/看/追踪），保留最关键的一个标识符，其余省略。禁止主语和标点。

示例：
kubectl get pods -n mt-prod  → 查 mt-prod Pods
git log -S QuickReply        → 追 QuickReply 来源
git blame RuleActionBar.tsx  → 查 RuleActionBar 作者
code-search ErrorHandler     → 搜 ErrorHandler
gcp-logs wati-gke            → 读 wati-gke 日志
github-workflow_runs         → 查 CI 构建
notion-search                → 搜 Notion 文档
web-search                   → 网络搜索

工具：%s
参数：%s`, names, sampleArgs)
	}
	return fmt.Sprintf(`Output a ≤8-word action label. One verb + one key identifier from the args. No subject, no punctuation.

Examples:
kubectl get pods -n mt-prod  → Checking mt-prod pods
git log -S QuickReply        → Tracing QuickReply origin
git blame RuleActionBar.tsx  → Finding RuleActionBar author
code-search ErrorHandler     → Searching ErrorHandler
gcp-logs wati-gke            → Reading wati-gke logs
github-workflow_runs         → Checking CI builds
notion-search                → Searching Notion

Tools: %s
Args: %s`, names, sampleArgs)
}

// sanitizeArgs trims the raw args JSON to a compact, summarizer-friendly form.
// Technical parameters (shell commands, repo paths, namespaces) are kept intact
// because they provide the specific context that makes summaries useful. Only
// long free-text fields that likely contain the user's verbatim question are
// truncated to prevent the summarizer from paraphrasing user intent.
func sanitizeArgs(args string) string {
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
