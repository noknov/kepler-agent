package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaticSystemPromptIsMemoizedUntilReload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte("memoized system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDirs(PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	first := StaticSystemPrompt()
	second := StaticSystemPrompt()
	if first != second {
		t.Fatalf("StaticSystemPrompt() should return cached value")
	}
	if first == "" {
		t.Fatal("StaticSystemPrompt() should not be empty")
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "agent.md"), []byte("reloaded system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDirs(PublicDir, other); err != nil {
		t.Fatal(err)
	}
	reloaded := StaticSystemPrompt()
	if reloaded == first {
		t.Fatalf("StaticSystemPrompt() should rebuild after LoadDirs")
	}
}

func TestDynamicSystemPromptOmitsEmptyInventory(t *testing.T) {
	if got := DynamicSystemPrompt(""); got != "" {
		t.Fatalf("DynamicSystemPrompt(\"\") = %q, want empty", got)
	}
}

func TestDynamicSystemPromptIncludesBoundaryMarker(t *testing.T) {
	got := DynamicSystemPrompt("- repo-a/ (Go)")
	if got == "" {
		t.Fatal("DynamicSystemPrompt() should not be empty")
	}
	if got[:len(DynamicBoundaryMarker)] != DynamicBoundaryMarker {
		t.Fatalf("DynamicSystemPrompt() should start with boundary marker: %q", got)
	}
}
