package agent

import (
	"math/rand"
	"unicode"

	"github.com/wati/oncall-agent/internal/prompts"
)

const (
	LocaleZH = "zh"
	LocaleEN = "en"
)

func DetectLocale(text string) string {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return LocaleZH
		}
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
}

var statusZH = statusSet{
	analyzing: []string{
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
	complete: "传输完毕",
	waiting:  "等待信号",
	failed:   "链路中断",
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
	complete: "Done",
	waiting:  "Waiting for your reply",
	failed:   "Analysis failed",
}

var toolHintsZH = map[string][]string{
	"code-read_file":                {"扫描源码中..."},
	"code-search":                   {"全文检索中..."},
	"code-symbols":                  {"解析符号表..."},
	"code-definition":               {"追踪定义源..."},
	"code-references":               {"扫描引用链..."},
	"code-diagnostics":              {"诊断代码中..."},
	"repo-search":                   {"仓库全文检索中..."},
	"repo-read_file":                {"读取文件快照..."},
	"git-status":                    {"读取仓库状态..."},
	"git-fetch_ref":                 {"拉取远程分支..."},
	"git-search_ref":                {"分支内检索中..."},
	"git-read_file_ref":             {"读取分支文件..."},
	"git-log":                       {"回溯时间线..."},
	"git-show":                      {"定位变更帧..."},
	"gcp-logs":                      {"日志流抓取中..."},
	"gcp-query_logs":                {"日志流抓取中..."},
	"notion-search":                 {"知识库索引中..."},
	"notion-create_page":            {"写入文档..."},
	"youtrack-get_issue":            {"加载工单..."},
	"youtrack-search":               {"检索工单库..."},
	"github-dispatch_workflow":      {"触发工作流..."},
	"github-workflow_runs":          {"读取流水线状态..."},
	"github-pr_diff":                {"拉取 PR 变更..."},
	"slack-ask_user":                {"等待外部输入..."},
	"slack-json_analyze":            {"解析数据文件..."},
	"slack-file_search":             {"检索附件中..."},
	"knowledge-runbook_search":      {"查阅运维手册..."},
	"diagnostics-incident_brief":    {"梳理事件摘要..."},
	"diagnostics-timeline":          {"重建时间线..."},
	"diagnostics-evidence_board":    {"整理证据链..."},
	"delegate-run":                  {"子进程展开中..."},
}

var toolHintsEN = map[string]string{
	"code-read_file":                "Reading source...",
	"code-search":                   "Searching codebase...",
	"code-symbols":                  "Resolving symbols...",
	"code-definition":               "Looking up definition...",
	"code-references":               "Finding references...",
	"code-diagnostics":              "Running diagnostics...",
	"repo-search":                   "Searching repo...",
	"repo-read_file":                "Reading file...",
	"git-status":                    "Checking repo state...",
	"git-fetch_ref":                 "Fetching remote ref...",
	"git-search_ref":                "Searching branch...",
	"git-read_file_ref":             "Reading file at ref...",
	"git-log":                       "Tracing commit history...",
	"git-show":                      "Inspecting changeset...",
	"gcp-logs":                      "Fetching log stream...",
	"gcp-query_logs":                "Fetching log stream...",
	"notion-search":                 "Searching knowledge base...",
	"notion-create_page":            "Writing document...",
	"youtrack-get_issue":            "Loading issue...",
	"youtrack-search":               "Searching issues...",
	"github-dispatch_workflow":      "Dispatching workflow...",
	"github-workflow_runs":          "Checking pipeline status...",
	"github-pr_diff":                "Fetching PR diff...",
	"slack-ask_user":                "Awaiting input...",
	"slack-json_analyze":            "Analyzing data file...",
	"slack-file_search":             "Searching attachments...",
	"knowledge-runbook_search":      "Searching runbooks...",
	"diagnostics-incident_brief":    "Summarizing incident...",
	"diagnostics-timeline":          "Building timeline...",
	"diagnostics-evidence_board":    "Gathering evidence...",
	"delegate-run":                  "Spawning sub-analysis...",
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

func ToolHint(name, locale string) string {
	if locale == LocaleZH {
		if hints, ok := toolHintsZH[name]; ok {
			return prompts.ToolStatus(name, pick(hints))
		}
		return prompts.ToolStatus("default", pick(statusZH.step))
	}
	if hint, ok := toolHintsEN[name]; ok {
		return prompts.ToolStatus(name, hint)
	}
	return prompts.ToolStatus("default", "Thinking...")
}
