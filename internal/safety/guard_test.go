package safety

import "testing"

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
