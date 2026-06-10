package prompts

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadDirLoadsRulesAndSkillMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills", "incident-review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules", "tone.md"), []byte("Prefer concise updates.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillBody := `---
name: incident-review
description: Use for incident reviews.
---

# Incident Review

Full workflow body.
`
	if err := os.WriteFile(filepath.Join(dir, "skills", "incident-review", "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDir(t.TempDir()) })

	got := RulesAndSkillsPrompt()
	for _, want := range []string{
		"Additional rules:",
		"# tone.md",
		"Prefer concise updates.",
		"Available skills:",
		"Use for incident reviews.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RulesAndSkillsPrompt() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Full workflow body.") {
		t.Fatalf("RulesAndSkillsPrompt() should not include full skill body:\n%s", got)
	}
	skill, ok := LoadSkill("incident-review")
	if !ok {
		t.Fatal("LoadSkill() did not find incident-review")
	}
	if skill.Name != "incident-review" || skill.Description != "Use for incident reviews." {
		t.Fatalf("skill metadata = %#v", skill)
	}
	if !strings.Contains(skill.Content, "Full workflow body.") {
		t.Fatalf("LoadSkill() did not return body:\n%s", skill.Content)
	}
}
