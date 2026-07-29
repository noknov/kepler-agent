package gitcache

import (
	"strings"
	"testing"
)

func TestGitCommandArgsUsesGitHubTokenWithoutEmbeddingSecret(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token")

	args := gitCommandArgs("-C", "/repo", "fetch", "origin")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "credential.helper") {
		t.Fatalf("git args should configure a credential helper: %v", args)
	}
	if strings.Contains(joined, "secret-token") {
		t.Fatalf("git args should not expose token in process arguments: %v", args)
	}
}

func TestGitCommandArgsLeavesPublicFetchUnchangedWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	args := gitCommandArgs("-C", "/repo", "fetch", "origin")
	if got, want := strings.Join(args, " "), "-C /repo fetch origin"; got != want {
		t.Fatalf("git args = %q, want %q", got, want)
	}
}
