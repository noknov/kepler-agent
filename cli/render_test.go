package cli

import (
	"strings"
	"testing"
)

func TestToolArgSummaryPrefersTaskNotRawJSON(t *testing.T) {
	raw := []byte(`{"boundaries":"仅阅读，不修改文件。检查仓库结构、instagram-service","task":"看一下 instagram-service"}`)
	got := toolArgSummary(raw)
	if got != "看一下 instagram-service" {
		t.Fatalf("summary = %q", got)
	}
}

func TestToolDisplayNameExplore(t *testing.T) {
	if toolDisplayName("agent-explore") != "Explore" {
		t.Fatalf("name = %q", toolDisplayName("agent-explore"))
	}
}

func TestToolArgSummaryDoesNotDumpJSON(t *testing.T) {
	raw := []byte(`{"boundaries":"仅阅读 instagram-service"}`)
	got := toolArgSummary(raw)
	if strings.Contains(got, "{") || got != "仅阅读 instagram-service" {
		t.Fatalf("summary = %q", got)
	}
}
