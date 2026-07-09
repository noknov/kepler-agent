package agent

import (
	"strings"
	"testing"
)

func TestSummarizePromptContainsNoLeakInstruction(t *testing.T) {
	prompt := summarizePrompt("web-search, github-pr_diff", `{"query":"Instagram Webhook docs"}`, LocaleZH)
	if !strings.Contains(prompt, "禁止在输出中包含工具名") {
		t.Fatal("ZH prompt missing no-leak instruction")
	}

	promptEN := summarizePrompt("k8s-pod-logs", `{"namespace":"prod"}`, "en")
	if !strings.Contains(promptEN, "Never include tool names") {
		t.Fatal("EN prompt missing no-leak instruction")
	}
}
