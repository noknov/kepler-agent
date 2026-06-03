package safety

import (
	"strings"
	"testing"
)

func TestSystemPromptFallbackStaysMinimal(t *testing.T) {
	prompt := (PromptPolicy{}).SystemPrompt()
	if !strings.Contains(prompt, "Slack assistant") {
		t.Fatalf("SystemPrompt() should keep a minimal fallback assistant role: %q", prompt)
	}
	if strings.Contains(prompt, "Channel-X Copilot Agent") || strings.Contains(prompt, "U085SRJFCLX") {
		t.Fatalf("SystemPrompt() should not contain deployment-specific identity prompt: %q", prompt)
	}
	if strings.Contains(prompt, "food or drink ordering") || strings.Contains(prompt, "author") {
		t.Fatalf("SystemPrompt() should not contain detailed product prompt text: %q", prompt)
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
