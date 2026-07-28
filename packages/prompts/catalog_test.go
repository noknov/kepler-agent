package prompts

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDirAppendsSystemPromptAndOverridesStructuredPrompts(t *testing.T) {
	public := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(public, "agent.md"), []byte("public system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte("private addendum\n"), 0o644); err != nil {
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

	if err := LoadDirs(public, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	if got := System("fallback"); got != "public system\n\nprivate addendum" {
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

func TestAgentPromptAppendsToSystemPromptWithinSameDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "system.md"), []byte("legacy system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte("agent system\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadDirs(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	if got := System("fallback"); got != "legacy system\n\nagent system" {
		t.Fatalf("System() = %q", got)
	}
}

func TestPublicPromptFilesAreRuntimeContent(t *testing.T) {
	publicDir := resolveDir(PublicDir)
	var readmes []string
	err := filepath.WalkDir(publicDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(strings.ToLower(filepath.Base(path)), "readme") {
			readmes = append(readmes, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readmes) > 0 {
		t.Fatalf("document prompt conventions in the root README, not under %s: %v", publicDir, readmes)
	}

	jsonFiles, err := filepath.Glob(filepath.Join(publicDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range jsonFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		if len(obj) == 0 {
			t.Fatalf("%s is an empty placeholder; remove it or add runtime content", path)
		}
	}

	runbookDocs, err := filepath.Glob(filepath.Join(publicDir, "runbooks", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range runbookDocs {
		if strings.EqualFold(filepath.Base(path), "README.md") {
			t.Fatalf("%s can be returned by runbook search; document runbook conventions in README instead", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(data))
		for _, marker := range []string{"placeholder", "todo", "replace this", "write xxx", "请写"} {
			if strings.Contains(lower, marker) {
				t.Fatalf("%s contains placeholder marker %q", path, marker)
			}
		}
	}
}

func TestRuntimeJSONOverridesPromptSections(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{
		"runner": {"empty_response_retry": "retry from runtime"},
		"texts": {"rules_header": "Rules from runtime:\n"},
		"app_messages": {"empty_mention": "hello"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadDirs(PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	if got := RunnerPrompt("empty_response_retry", "fallback"); got != "retry from runtime" {
		t.Fatalf("RunnerPrompt() = %q", got)
	}
	if got := PromptText("rules_header", "fallback"); got != "Rules from runtime:\n" {
		t.Fatalf("PromptText() = %q", got)
	}
	if got := AppMessage("empty_mention", "fallback"); got != "hello" {
		t.Fatalf("AppMessage() = %q", got)
	}
}

func TestPrivateRulesOverrideByFilename(t *testing.T) {
	public := t.TempDir()
	private := t.TempDir()
	if err := os.MkdirAll(filepath.Join(public, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(private, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "rules", "general.md"), []byte("public rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "rules", "general.md"), []byte("private rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "rules", "local.md"), []byte("local rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadDirs(public, private); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	got := RulesPrompt()
	if strings.Contains(got, "public rule") {
		t.Fatalf("RulesPrompt() should replace same-name public rule:\n%s", got)
	}
	for _, want := range []string{"private rule", "local rule"} {
		if !strings.Contains(got, want) {
			t.Fatalf("RulesPrompt() missing %q:\n%s", want, got)
		}
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

	if err := LoadDirs(PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

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

func TestLoadDirParsesMultilineSkillDescription(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "zhangxuefeng-zhiyuan"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillBody := `---
name: zhangxuefeng-zhiyuan
description: |
  张雪峰的思维框架与表达方式，专注高考志愿填报与职业规划。
  当用户提到「高考」「志愿」「填报」「选专业」「分数线」「位次」时使用。
---

# Body
`
	if err := os.WriteFile(filepath.Join(dir, "skills", "zhangxuefeng-zhiyuan", "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadDirs(PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadDirs(PublicDir) })

	got := SkillsPrompt()
	for _, want := range []string{"zhangxuefeng-zhiyuan", "高考志愿填报", "分数线", "位次"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SkillsPrompt() missing %q:\n%s", want, got)
		}
	}
	skill, ok := LoadSkill("zhangxuefeng-zhiyuan")
	if !ok {
		t.Fatal("LoadSkill() did not find zhangxuefeng-zhiyuan")
	}
	if !strings.Contains(skill.Description, "当用户提到") {
		t.Fatalf("multiline description not parsed: %#v", skill)
	}
}
