package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(got, "<persisted-output>") || !strings.Contains(got, "Full output saved to") {
		t.Fatalf("spilled content should include reference, got %q", got)
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
