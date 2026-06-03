package safety

import (
	"strings"
	"testing"
)

func TestSystemPromptIsGeneralPurposeAssistant(t *testing.T) {
	prompt := (PromptPolicy{}).SystemPrompt()
	if !strings.Contains(prompt, "general-purpose assistant") {
		t.Fatalf("SystemPrompt() should describe a general-purpose assistant: %q", prompt)
	}
	if !strings.Contains(prompt, "Channel-X Copilot Agent") || !strings.Contains(prompt, "<@U085SRJFCLX>") {
		t.Fatalf("SystemPrompt() should include agent identity and author: %q", prompt)
	}
	if !strings.Contains(prompt, "food or drink ordering") {
		t.Fatalf("SystemPrompt() should cover future local-life tools: %q", prompt)
	}
	if !strings.Contains(prompt, "requires explicit user confirmation") {
		t.Fatalf("SystemPrompt() should require confirmation before risky actions: %q", prompt)
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
