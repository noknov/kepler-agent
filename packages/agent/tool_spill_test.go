package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestMaybeSpillResultSmallContent(t *testing.T) {
	content := strings.Repeat("a", 100)
	if got := maybeSpillResult(context.Background(), nil, "run-test", "code-read_file", "call12345", content); got != content {
		t.Fatalf("small content should pass through unchanged")
	}
}

func TestMaybeSpillResultLargeContent(t *testing.T) {
	dir := filepath.Join(spillDir, "run-spill-test")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	content := strings.Repeat("x", maxToolResultChars+1000)
	got := maybeSpillResult(context.Background(), nil, "run-spill-test", "code-read_file", "call12345678", content)
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

func TestMaybeSpillResultFallbackKeepsUTF8Valid(t *testing.T) {
	got := truncateRunes(strings.Repeat("界", maxToolResultChars+10), maxToolResultChars)
	if !utf8.ValidString(got) {
		t.Fatalf("fallback truncation produced invalid UTF-8")
	}
	if len([]rune(got)) != maxToolResultChars {
		t.Fatalf("rune length = %d, want %d", len([]rune(got)), maxToolResultChars)
	}
}

func TestSpillReadToolReadsQuerySlice(t *testing.T) {
	runID := "run-spill-read-test"
	dir := filepath.Join(spillDir, runID)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	content := strings.Repeat("a", maxToolResultChars) + " before NEEDLE after " + strings.Repeat("z", 2000)
	notice := maybeSpillResult(context.Background(), nil, runID, "code-read_file", "callabcdef123", content)
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

func TestMaybeSpillResultUsesStore(t *testing.T) {
	store := &fakeSpillStore{items: map[string]string{}}
	content := strings.Repeat("x", maxToolResultChars+1000)
	notice := maybeSpillResult(context.Background(), store, "run-pg-spill-test", "notion-get_page", "call12345678", content)
	if !strings.Contains(notice, "<persisted-output>") {
		t.Fatalf("spill notice missing persisted output marker: %q", notice)
	}
	if got := store.items["run-pg-spill-test/notion-get_page/call12345678"]; got != content {
		t.Fatal("spill store should contain full content")
	}

	raw, _ := json.Marshal(map[string]any{
		"tool_name":    "notion-get_page",
		"tool_call_id": "call12345678",
		"offset":       maxToolResultChars + 900,
		"limit":        200,
	})
	result, err := (SpillReadTool{}).Execute(context.Background(), raw, registry.Runtime{RunID: "run-pg-spill-test", ToolSpillStore: store})
	if err != nil {
		t.Fatalf("SpillReadTool.Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, strings.Repeat("x", 10)) {
		t.Fatalf("spill read result did not include stored content: %q", result.Content)
	}
}

type fakeSpillStore struct {
	items map[string]string
}

func (s *fakeSpillStore) SaveToolSpill(_ context.Context, runID, toolName, toolCallID, content string) error {
	s.items[runID+"/"+toolName+"/"+toolCallID] = content
	return nil
}

func (s *fakeSpillStore) ReadToolSpill(_ context.Context, runID, toolName, toolCallID string) (string, error) {
	return s.items[runID+"/"+toolName+"/"+toolCallID], nil
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
