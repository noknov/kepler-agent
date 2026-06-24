package agent

import (
	"strings"
	"testing"
)

func TestToolHintDefaultUsesChineseForZHLocale(t *testing.T) {
	got := ToolHint("unknown-tool", LocaleZH)
	if got == "Thinking..." {
		t.Fatalf("ToolHint() = %q, want localized Chinese status", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("ToolHint() = %q, want step-style status text", got)
	}
}

func TestToolHintDefaultUsesThinkingForENLocale(t *testing.T) {
	got := ToolHint("unknown-tool", LocaleEN)
	if got != "Thinking..." {
		t.Fatalf("ToolHint() = %q, want Thinking...", got)
	}
}

func TestToolHintExploreCode(t *testing.T) {
	if got := ToolHint("explore-code", LocaleEN); got != "Exploring codebase..." {
		t.Fatalf("ToolHint(explore-code, en) = %q", got)
	}
	if got := ToolHint("explore-code", LocaleZH); got != "代码探索中..." {
		t.Fatalf("ToolHint(explore-code, zh) = %q", got)
	}
}

func TestStepStatusZHIncludesThinking(t *testing.T) {
	choices := map[string]struct{}{}
	for range 50 {
		choices[StepStatus(LocaleZH, 0)] = struct{}{}
	}
	if _, ok := choices["思考中..."]; !ok {
		t.Fatalf("StepStatus(zh, 0) never returned 思考中..., got %#v", choices)
	}
}
