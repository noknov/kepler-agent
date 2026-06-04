package git

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestFetchRefReturnsImmutableCommitRef(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	runGit(t, work, "remote", "add", "origin", origin)
	runGit(t, work, "push", "origin", "main")

	tool := FetchRefTool{Base: Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	content := result.Content
	if !strings.Contains(content, "branch_ref=origin/main") {
		t.Fatalf("content = %q, want branch_ref", content)
	}
	match := regexp.MustCompile(`(?m)^ref=([0-9a-f]{40})$`).FindStringSubmatch(content)
	if match == nil {
		t.Fatalf("content = %q, want immutable commit SHA ref", content)
	}
	for _, line := range strings.Split(content, "\n") {
		if line == "ref=origin/main" {
			t.Fatalf("content = %q, ref should not be a moving branch ref", content)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}
