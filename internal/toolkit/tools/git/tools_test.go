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
	"github.com/wati/oncall-agent/internal/toolkit/gitcache"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestFetchRefReturnsImmutableCommitRef(t *testing.T) {
	root, work := testRepo(t)

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
	if !strings.Contains(content, "repo=work") {
		t.Fatalf("content = %q, want repo label", content)
	}
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

func TestFetchRefUsesOriginHEADDefaultBranch(t *testing.T) {
	root, work := testRepo(t)
	runGit(t, work, "branch", "-m", "main", "mt-main")
	runGit(t, work, "push", "origin", ":main", "mt-main")
	runGit(t, work, "fetch", "origin")
	runGit(t, work, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/mt-main")

	tool := FetchRefTool{Base: Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "branch=mt-main") || !strings.Contains(result.Content, "branch_ref=origin/mt-main") {
		t.Fatalf("content = %q, want origin/HEAD default branch mt-main", result.Content)
	}
}

func TestFetchRefUsesPRHeadBranchWhenBranchOmitted(t *testing.T) {
	root, work := testRepo(t)
	runGit(t, work, "checkout", "-b", "feature")
	runGit(t, work, "push", "origin", "feature")
	head := strings.TrimSpace(runGitOutput(t, work, "rev-parse", "HEAD"))
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}
	registry.SetPRContext(rt, registry.PRContext{Repository: "example/work", RepoPath: "work", HeadRef: "feature", HeadSHA: head})
	tool := FetchRefTool{Base: Base{Paths: safety.WorkspacePolicy{Roots: []string{root}}, Guard: safety.NewCommandPolicy(), Timeout: 10 * time.Second}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "branch=feature") || !strings.Contains(result.Content, "ref="+head) {
		t.Fatalf("PR fetch must use the head branch, got %q", result.Content)
	}
}

func TestFetchRefUsesProcessFetchCacheWithinTTL(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}
	tool := FetchRefTool{Base: base}

	first, err := tool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Content, "branch=main") {
		t.Fatalf("first content = %q, want branch snapshot", first.Content)
	}
	if err := os.Rename(filepath.Join(root, "origin.git"), filepath.Join(root, "origin.git.offline")); err != nil {
		t.Fatal(err)
	}

	second, err := tool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), rt)
	if err != nil {
		t.Fatalf("second fetch should reuse process cache within TTL, got %v", err)
	}
	if !strings.Contains(second.Content, "branch=main") {
		t.Fatalf("second content = %q, want cached branch snapshot", second.Content)
	}
}

func TestRefToolsRequireExplicitRepo(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	fetchResult, err := (FetchRefTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	ref := regexp.MustCompile(`(?m)^ref=([0-9a-f]{40})$`).FindStringSubmatch(fetchResult.Content)[1]

	_, err = (ReadFileRefTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"ref":"`+ref+`","path":"README.md"}`), registry.Runtime{})
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Fatalf("ReadFileRefTool error = %v, want repo required", err)
	}
	_, err = (SearchRefTool{Base: base}).Execute(context.Background(), json.RawMessage(`{"ref":"`+ref+`","query":"hello"}`), registry.Runtime{})
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Fatalf("SearchRefTool error = %v, want repo required", err)
	}
}

func TestRepoToolsReadFetchedSnapshotNotWorkingTree(t *testing.T) {
	root, work := testRepo(t)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("local only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	searchTool := RepoSearchTool{Base: base}
	searchResult, err := searchTool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","query":"hello"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(searchResult.Content, "README.md:1:hello") {
		t.Fatalf("search content = %q, want remote snapshot match", searchResult.Content)
	}
	noMatch, err := searchTool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","query":"local only"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noMatch.Content, "no matches") {
		t.Fatalf("search content = %q, want no local working tree match", noMatch.Content)
	}

	readTool := RepoReadFileTool{Base: base}
	readResult, err := readTool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","path":"README.md"}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readResult.Content, "hello") || strings.Contains(readResult.Content, "local only") {
		t.Fatalf("read content = %q, want remote snapshot only", readResult.Content)
	}
}

func TestRepoToolsUseProcessFetchCacheWithinTTL(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}
	searchTool := RepoSearchTool{Base: base}
	if _, err := searchTool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","query":"hello"}`), rt); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "origin.git"), filepath.Join(root, "origin.git.offline")); err != nil {
		t.Fatal(err)
	}

	readTool := RepoReadFileTool{Base: base}
	result, err := readTool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","path":"README.md"}`), rt)
	if err != nil {
		t.Fatalf("read should reuse process cache within TTL after origin becomes unavailable: %v", err)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Fatalf("read content = %q, want cached remote snapshot", result.Content)
	}

	gitcache.ResetForTest()
	stale, err := readTool.Execute(context.Background(), json.RawMessage(`{"repo":"`+work+`","path":"README.md"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatalf("read with expired process cache should fall back to cached refs while origin is unavailable: %v", err)
	}
	if !strings.Contains(stale.Content, "refresh_failed_using_cached_refs") {
		t.Fatalf("read content = %q, want stale fetch status", stale.Content)
	}
}

func testRepo(t *testing.T) (string, string) {
	t.Helper()
	gitcache.ResetForTest()
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
	return root, work
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

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}
