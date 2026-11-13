package safety

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/environment"
	"github.com/noknov/kepler-agent/packages/prompts"
)

func TestSystemPromptDefaultStaysGeneric(t *testing.T) {
	if err := prompts.LoadDirs(prompts.PublicDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prompts.LoadDirs(prompts.PublicDir) })

	prompt := (PromptPolicy{}).SystemPrompt()
	if !strings.Contains(prompt, "general-purpose intelligent assistant") {
		t.Fatalf("SystemPrompt() should keep the public default assistant role: %q", prompt)
	}
	if strings.Contains(prompt, "channelx-kepler-agent") || strings.Contains(prompt, "Channel-X Copilot Agent") || strings.Contains(prompt, "U085SRJFCLX") {
		t.Fatalf("SystemPrompt() should not contain deployment-specific identity prompt: %q", prompt)
	}
	if strings.Contains(prompt, "food or drink ordering") {
		t.Fatalf("SystemPrompt() should not contain detailed product prompt text: %q", prompt)
	}
	if strings.Contains(prompt, "<environment_context>") || strings.Contains(prompt, "Runtime context:") {
		t.Fatalf("SystemPrompt() should remain static and omit runtime environment facts: %q", prompt)
	}
}

func TestEnvironmentMessageIncludesRuntimeDateContext(t *testing.T) {
	now := time.Date(2026, 6, 30, 9, 15, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	text := (environment.Config{
		WorkspaceRoots: []string{"/data/workspace"},
		Now:            func() time.Time { return now },
	}).Message().Text()
	for _, want := range []string{
		"<environment_context>",
		"<current_date>2026-06-30</current_date>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("environment message missing %q:\n%s", want, text)
		}
	}
}

func TestSystemPromptIncludesConfiguredSkills(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "triage"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: triage
description: Use this for alert triage.
---

# Triage

Full triage workflow body.
`
	if err := os.WriteFile(filepath.Join(dir, "skills", "triage", "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prompts.LoadDirs(prompts.PublicDir, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prompts.LoadDirs(prompts.PublicDir) })

	prompt := (PromptPolicy{}).SystemPrompt()
	if !strings.Contains(prompt, "Available skills:") || !strings.Contains(prompt, "Use this for alert triage.") {
		t.Fatalf("SystemPrompt() did not include configured skill:\n%s", prompt)
	}
	if strings.Contains(prompt, "Full triage workflow body.") {
		t.Fatalf("SystemPrompt() should not include full skill body:\n%s", prompt)
	}
}

func TestRedactorRedactsSecrets(t *testing.T) {
	got := (Redactor{}).Sanitize("Authorization: Bearer sk-abc and SLACK_TOKEN=xoxb-secret and gh=ghp_1234567890abcdefghijklmnop and db=postgres://admin:supersecret@db.internal:5432/app")
	if strings.Contains(got, "sk-abc") || strings.Contains(got, "xoxb-secret") || strings.Contains(got, "ghp_1234567890") || strings.Contains(got, "supersecret") {
		t.Fatalf("expected redaction, got %q", got)
	}
}

func TestWorkspacePolicyRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := (WorkspacePolicy{Roots: []string{root}}).ResolveReadableFile("link.txt")
	if err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestWorkspacePolicyRejectsSymlinkDirectoryEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := (WorkspacePolicy{Roots: []string{root}}).ResolveReadablePath("linked"); err == nil {
		t.Fatal("expected symlink directory escape to be rejected")
	}
}

func TestSensitivePathIncludesCredentialFilesAndDirectories(t *testing.T) {
	for _, path := range []string{".npmrc", "project/.netrc", "secrets", "secrets/nested/file.txt", ".docker/config.json"} {
		if !IsSensitivePath(path) {
			t.Errorf("IsSensitivePath(%q) = false", path)
		}
	}
}

func TestWorkspacePolicyRejectsSensitivePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "private.pem"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (WorkspacePolicy{Roots: []string{root}}).ResolveReadableFile("private.pem")
	if err == nil {
		t.Fatal("expected sensitive path to be rejected")
	}
}

func TestWorkspacePolicyRejectsSymlinkToSensitivePathInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "notes.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := (WorkspacePolicy{Roots: []string{root}}).ResolveReadableFile("notes.txt"); err == nil {
		t.Fatal("expected symlink to sensitive in-workspace target to be rejected")
	}
}
