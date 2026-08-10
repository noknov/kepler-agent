package local

import (
	"strings"
	"testing"
)

func TestSafeEnvironmentDoesNotInheritCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("PATH", "/bin")
	environment := safeEnvironment("/isolated", nil)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "OPENAI_API_KEY") {
		t.Fatalf("credential leaked: %s", joined)
	}
	if !strings.Contains(joined, "HOME=/isolated") || !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("missing safe values: %s", joined)
	}
}
