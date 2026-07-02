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
		return fmt.Sprintf(`根据工具名和参数，用5-8个字描述AI当前执行的具体操作。

规则：
- 格式：动词 + 具体对象（从参数中提取真实名称：仓库名、命名空间、服务名、文件名、关键词等）
- 禁止：主语、标点、"您想/AI正在"等前缀、模糊宾语（"代码"/"文件"/"内容"）
- 参数里的具体名称优先于工具名的通用含义

示例：
shell "kubectl get pods -n mt-prod" → 查询 mt-prod Pod 状态
shell "git log -S QuickReply"       → 追踪 QuickReply 变更来源
shell "git blame RuleActionBar.tsx" → 追溯 RuleActionBar 修改者
shell "git grep InstagramComment"   → 搜索 InstagramComment 引用
code-search pattern=ErrorHandler    → 搜索 ErrorHandler 实现
gcp-logs project=wati-gke           → 读取 wati-gke 错误日志
github-pr_diff repo=wati-frontend   → 查看 wati-frontend PR 差异
github-workflow_runs                → 检查 CI 构建状态
notion-search                       → 检索 Notion 文档
web-search                          → 网络搜索

工具：%s
参数：%s`, names, sampleArgs)
	}
	return fmt.Sprintf(`Summarize the AI's current action in 5-8 words. Extract specific names from the args.

Rules:
- Format: verb + specific object (use real names from args: repo, namespace, service, file, keyword)
- No subject, no punctuation, no "You want to" or "The user wants"
- Specific names from args > generic inference from tool name

Examples:
shell "kubectl get pods -n mt-prod" → Checking pod status in mt-prod
shell "git log -S QuickReply"       → Tracing QuickReply change history
shell "git blame RuleActionBar.tsx" → Finding author of RuleActionBar
code-search pattern=ErrorHandler    → Searching ErrorHandler implementation
gcp-logs project=wati-gke           → Reading wati-gke error logs
github-pr_diff repo=wati-frontend   → Reviewing wati-frontend PR diff
notion-search                       → Searching Notion workspace

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
