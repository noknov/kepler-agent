package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/prompts"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestLoadToolReturnsSkillBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: demo
description: Use for demo tasks.
---

# Demo

Follow these detailed steps.
`
	if err := os.WriteFile(filepath.Join(dir, "skills", "demo", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prompts.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prompts.LoadDirs(prompts.PublicDir) })

	result, err := LoadTool{}.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Follow these detailed steps.") {
		t.Fatalf("skill body missing:\n%s", result.Content)
	}
}
