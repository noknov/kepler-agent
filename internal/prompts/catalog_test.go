package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirOverridesPrompts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "system.md"), []byte("system override\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools.json"), []byte(`{
		"demo-tool": {
			"description": "tool override",
			"parameters": {"query": "query override"}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "delegates.json"), []byte(`{"code":"delegate override"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDir(t.TempDir()) })

	if got := System("fallback"); got != "system override" {
		t.Fatalf("System() = %q", got)
	}
	if got := ToolDescription("demo-tool", "fallback"); got != "tool override" {
		t.Fatalf("ToolDescription() = %q", got)
	}
	if got := ParamDescription("demo-tool", "query", "fallback"); got != "query override" {
		t.Fatalf("ParamDescription() = %q", got)
	}
	if got := Delegate("code", "fallback"); got != "delegate override" {
		t.Fatalf("Delegate() = %q", got)
	}
}
