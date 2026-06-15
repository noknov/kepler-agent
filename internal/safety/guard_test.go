package safety

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/prompts"
)

func TestSystemPromptDefaultStaysGeneric(t *testing.T) {
	if err := prompts.LoadDirs(prompts.PublicDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prompts.LoadDirs(prompts.PublicDir) })

	prompt := (PromptPolicy{}).SystemPrompt()
	if !strings.Contains(prompt, "capable engineering assistant") {
		t.Fatalf("SystemPrompt() should keep the public default assistant role: %q", prompt)
	}
	if strings.Contains(prompt, "channelx-copilot-agent") || strings.Contains(prompt, "Channel-X Copilot Agent") || strings.Contains(prompt, "U085SRJFCLX") {
		t.Fatalf("SystemPrompt() should not contain deployment-specific identity prompt: %q", prompt)
	}
	if strings.Contains(prompt, "food or drink ordering") || strings.Contains(prompt, "author") {
		t.Fatalf("SystemPrompt() should not contain detailed product prompt text: %q", prompt)
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
	got := (Redactor{}).Sanitize("Authorization: Bearer sk-abc and SLACK_TOKEN=xoxb-secret")
	if got == "Authorization: Bearer sk-abc and SLACK_TOKEN=xoxb-secret" {
		t.Fatal("expected redaction")
	}
}

func TestCommandPolicyBlocksDangerousCommands(t *testing.T) {
	guard := NewCommandPolicy()
	if err := guard.Check("git status --short"); err != nil {
		t.Fatalf("expected git status allowed: %v", err)
	}
	if err := guard.Check("kubectl delete pod api-1"); err == nil {
		t.Fatal("expected kubectl delete blocked")
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
