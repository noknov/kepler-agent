package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestMaybeSpillResultSmallContent(t *testing.T) {
	content := strings.Repeat("a", 100)
	if got := maybeSpillResult("run-test", "code-read_file", "call12345", content); got != content {
		t.Fatalf("small content should pass through unchanged")
	}
}

func TestMaybeSpillResultLargeContent(t *testing.T) {
	dir := filepath.Join(spillDir, "run-spill-test")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	content := strings.Repeat("x", maxToolResultChars+1000)
	got := maybeSpillResult("run-spill-test", "code-read_file", "call12345678", content)
	if got == content {
		t.Fatal("large content should be spilled")
	}
	if !strings.Contains(got, "<persisted-output>") || !strings.Contains(got, "Output too large") {
		t.Fatalf("spilled content should include truncation notice, got %q", got)
	}
	if strings.Contains(got, "saved to:") {
		t.Fatal("spill message should not expose file path to the model")
	}
	path := filepath.Join(dir, "code-read_file-call1234.txt")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected spill file at %s: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != content {
		t.Fatal("spill file should contain full content")
	}
}

func TestSpillReadToolReadsQuerySlice(t *testing.T) {
	runID := "run-spill-read-test"
	dir := filepath.Join(spillDir, runID)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	content := strings.Repeat("a", maxToolResultChars) + " before NEEDLE after " + strings.Repeat("z", 2000)
	notice := maybeSpillResult(runID, "code-read_file", "callabcdef123", content)
	if !strings.Contains(notice, "tool_spill-read") {
		t.Fatalf("spill notice should mention tool_spill-read, got %q", notice)
	}

	raw, _ := json.Marshal(map[string]any{
		"tool_name":    "code-read_file",
		"tool_call_id": "callabcdef123",
		"query":        "NEEDLE",
		"limit":        200,
	})
	result, err := (SpillReadTool{}).Execute(context.Background(), raw, registry.Runtime{RunID: runID})
	if err != nil {
		t.Fatalf("SpillReadTool.Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "NEEDLE") {
		t.Fatalf("spill read result did not include query hit: %q", result.Content)
	}
	if strings.Contains(result.Content, spillDir) {
		t.Fatalf("spill read result leaked storage path: %q", result.Content)
	}
}

func TestRequestLocalePrefersSession(t *testing.T) {
	if got := requestLocale("en"); got != "en" {
		t.Fatalf("requestLocale(en) = %q, want en", got)
	}
	if got := requestLocale(" zh "); got != "zh" {
		t.Fatalf("requestLocale( zh ) = %q, want zh", got)
	}
	if got := requestLocale(""); got != "" {
		t.Fatalf("requestLocale('') = %q, want empty string", got)
	}
}
