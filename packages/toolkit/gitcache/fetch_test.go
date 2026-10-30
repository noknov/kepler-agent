package gitcache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestFetchOriginCachesOnlyItsLocalCheckout(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--bare", "--initial-branch=main", remote)
	runGit("clone", remote, seed)
	runGit("-C", seed, "config", "user.name", "Fixture")
	runGit("-C", seed, "config", "user.email", "fixture@example.invalid")
	commit := func(message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(seed, "value"), []byte(message), 0600); err != nil {
			t.Fatal(err)
		}
		runGit("-C", seed, "add", "value")
		runGit("-C", seed, "commit", "-m", message)
		runGit("-C", seed, "push", "origin", "main")
		return runGit("-C", seed, "rev-parse", "HEAD")
	}
	first := commit("first")
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	runGit("clone", remote, a)
	runGit("clone", remote, b)
	if err := FetchOrigin(ctx, a, time.Hour); err != nil {
		t.Fatal(err)
	}
	second := commit("second")
	if err := FetchOrigin(ctx, b, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := runGit("-C", b, "rev-parse", "origin/main"); got != second {
		t.Fatalf("another checkout was skipped: %s", got)
	}
	if err := FetchOrigin(ctx, a, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := runGit("-C", a, "rev-parse", "origin/main"); got != first {
		t.Fatalf("local success TTL was ignored: %s", got)
	}
	if err := FetchOriginFresh(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := runGit("-C", a, "rev-parse", "origin/main"); got != second {
		t.Fatalf("forced refresh retained old refs: %s", got)
	}
	if err := os.Rename(remote, remote+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := FetchOriginFresh(ctx, a); err == nil {
		t.Fatal("failed fetch reported success")
	}
	if err := FetchOrigin(ctx, a, time.Hour); err == nil {
		t.Fatal("failed fetch became a cached success")
	}
}
