package agent

import (
	"math/rand"
	"unicode"

	"github.com/noknov/slack-copilot-agent/internal/prompts"
)

const (
	LocaleZH = "zh"
	LocaleEN = "en"
)

// DetectLocale checks whether the text contains any CJK characters.
// Used to determine the language for status hints. Defaults to Chinese
// since this is the primary language for our user base.
func DetectLocale(text string) string {
	hasCJK := false
	hasLatin := false
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			hasCJK = true
			break
		}
		if r >= 'A' && r <= 'z' {
			hasLatin = true
		}
	}
	if hasCJK || !hasLatin {
		return LocaleZH
	}
	return LocaleEN
}

type statusSet struct {
	analyzing  []string
	step       []string
	generating []string
	retrying   []string
	complete   string
	waiting    string
	failed     string
	canceling  string
	cancelled  string
	steering   string
}

var statusZH = statusSet{
	analyzing: []string{
		"思考中...",
		"少女祈祷中...",
		"量子纠缠中...",
		"神经元同步中...",
		"意识潜入中...",
		"信号解码中...",
	},
	step: []string{
		"深层推演中...",
		"维度展开中...",
		"回路收束中...",
		"逼近奇点...",
		"相位校准中...",
	},
	generating: []string{
		"凝结结论中...",
		"输出通道开启...",
	},
	retrying: []string{
		"路径重构中...",
		"切换频道...",
	},
	complete:  "传输完毕",
	waiting:   "等待信号",
	failed:    "链路中断",
	canceling: "中止中...",
	cancelled: "已中止",
	steering:  "对话引导中...",
}

var statusEN = statusSet{
	analyzing: []string{
		"Thinking...",
	},
	step: []string{
		"Still thinking...",
		"Digging deeper...",
		"Almost there...",
	},
	generating: []string{
		"Composing response...",
	},
	retrying: []string{
		"Rethinking...",
	},
	complete:  "Done",
	waiting:   "Waiting for your reply",
	failed:    "Analysis failed",
	canceling: "Cancelling...",
	cancelled: "Cancelled",
	steering:  "Steering...",
}

type toolHint struct {
	zh string
	en string
}

var toolHints = map[string]toolHint{
	"code-read_file":             {"扫描源码中...", "Reading source..."},
	"code-search":                {"全文检索中...", "Searching codebase..."},
	"code-symbols":               {"解析符号表...", "Resolving symbols..."},
	"code-definition":            {"追踪定义源...", "Looking up definition..."},
	"code-references":            {"扫描引用链...", "Finding references..."},
	"code-diagnostics":           {"诊断代码中...", "Running diagnostics..."},
	"repo-search":                {"仓库全文检索中...", "Searching repo..."},
	"repo-read_file":             {"读取文件快照...", "Reading file..."},
	"git-status":                 {"读取仓库状态...", "Checking repo state..."},
	"git-fetch_ref":              {"拉取远程分支...", "Fetching remote ref..."},
	"git-search_ref":             {"分支内检索中...", "Searching branch..."},
	"git-read_file_ref":          {"读取分支文件...", "Reading file at ref..."},
	"git-log":                    {"回溯时间线...", "Tracing commit history..."},
	"git-show":                   {"定位变更帧...", "Inspecting changeset..."},
	"gcp-logs":                   {"日志流抓取中...", "Fetching log stream..."},
	"gcp-query_logs":             {"日志流抓取中...", "Fetching log stream..."},
	"notion-search":              {"知识库索引中...", "Searching knowledge base..."},
	"notion-create_page":         {"写入文档...", "Writing document..."},
	"youtrack-get_issue":         {"加载工单...", "Loading issue..."},
	"youtrack-search":            {"检索工单库...", "Searching issues..."},
	"github-dispatch_workflow":   {"触发工作流...", "Dispatching workflow..."},
	"github-workflow_runs":       {"读取流水线状态...", "Checking pipeline status..."},
	"github-pr_diff":             {"拉取 PR 变更...", "Fetching PR diff..."},
	"github-job_logs":            {"拉取 CI 日志...", "Fetching CI logs..."},
	"slack-ask_user":             {"等待外部输入...", "Awaiting input..."},
	"slack-json_analyze":         {"解析数据文件...", "Analyzing data file..."},
	"slack-file_search":          {"检索附件中...", "Searching attachments..."},
	"knowledge-runbook_search":   {"查阅运维手册...", "Searching runbooks..."},
	"diagnostics-incident_brief": {"梳理事件摘要...", "Summarizing incident..."},
	"diagnostics-timeline":       {"重建时间线...", "Building timeline..."},
	"diagnostics-evidence_board": {"整理证据链...", "Gathering evidence..."},
	"plan-update":                {"整理计划中...", "Updating plan..."},
	"delegate-run":               {"子进程展开中...", "Spawning sub-analysis..."},
	"explore-code":               {"代码探索中...", "Exploring codebase..."},
}

func pick(choices []string) string {
	return choices[rand.Intn(len(choices))]
}

func localeSet(locale string) statusSet {
	if locale == LocaleZH {
		return statusZH
	}
	return statusEN
}

func ThinkingStatus(locale string) string {
	return localeSet(locale).analyzing[0]
}

func StepStatus(locale string, step int) string {
	s := localeSet(locale)
	if step == 0 {
		return pick(s.analyzing)
	}
	return pick(s.step)
}

func GeneratingStatus(locale string) string {
	return pick(localeSet(locale).generating)
}

func RetryStatus(locale string) string {
	return pick(localeSet(locale).retrying)
}

func CompleteTitle(locale string) string {
	return localeSet(locale).complete
}

func WaitingTitle(locale string) string {
	return localeSet(locale).waiting
}

func FailedTitle(locale string) string {
	return localeSet(locale).failed
}

func CancelingTitle(locale string) string {
	return localeSet(locale).canceling
}

func CancelledTitle(locale string) string {
	return localeSet(locale).cancelled
}

func SteeringQueuedTitle(locale string) string {
	return localeSet(locale).steering
}

func ToolHint(name, locale string) string {
	if h, ok := toolHints[name]; ok {
		if locale == LocaleZH {
			return prompts.ToolStatus(name, h.zh)
		}
		return prompts.ToolStatus(name, h.en)
	}
	if locale == LocaleZH {
		return pick(statusZH.step)
	}
	return prompts.ToolStatus("default", "Thinking...")
}
