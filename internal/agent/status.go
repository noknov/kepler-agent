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
	"code-read_file":           {"扫描源码中..."},
	"code-search":              {"全文检索中..."},
	"git-status":               {"读取仓库状态..."},
	"git-log":                  {"回溯时间线..."},
	"git-show":                 {"定位变更帧..."},
	"gcp-logs":                 {"日志流抓取中..."},
	"notion-search":            {"知识库索引中..."},
	"notion-create_page":       {"写入文档..."},
	"youtrack-get_issue":       {"加载工单..."},
	"youtrack-search":          {"检索工单库..."},
	"github-dispatch_workflow": {"触发工作流..."},
	"github-workflow_runs":     {"读取流水线状态..."},
	"slack-ask_user":           {"等待外部输入..."},
	"delegate-run":             {"子进程展开中..."},
}

var toolHintsEN = map[string]string{
	"code-read_file":           "Reading source...",
	"code-search":              "Searching codebase...",
	"git-status":               "Checking repo state...",
	"git-log":                  "Tracing commit history...",
	"git-show":                 "Inspecting changeset...",
	"gcp-logs":                 "Fetching log stream...",
	"gcp-query_logs":           "Fetching log stream...",
	"notion-search":            "Searching knowledge base...",
	"notion-create_page":       "Writing document...",
	"youtrack-get_issue":       "Loading issue...",
	"youtrack-search":          "Searching issues...",
	"github-dispatch_workflow": "Dispatching workflow...",
	"github-workflow_runs":     "Checking pipeline status...",
	"slack-ask_user":           "Awaiting input...",
	"delegate-run":             "Spawning sub-analysis...",
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
