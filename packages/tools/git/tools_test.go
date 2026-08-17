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

	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/gitcache"
	agenttool "github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

func TestFetchRefReturnsImmutableCommitRef(t *testing.T) {
	root, work := testRepo(t)

	tool := FetchRefTool{Base: Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	content := result.Text()
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

func TestFetchRefUsesExplicitBranch(t *testing.T) {
	root, work := testRepo(t)
	runGit(t, work, "branch", "-m", "main", "mt-main")
	runGit(t, work, "push", "origin", ":main", "mt-main")
	runGit(t, work, "fetch", "origin")

	tool := FetchRefTool{Base: Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"mt-main"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "branch=mt-main") || !strings.Contains(result.Text(), "branch_ref=origin/mt-main") {
		t.Fatalf("content = %q, want explicit branch mt-main", result.Text())
	}
}

func TestFetchRefFailsWhenOriginBecomesUnavailable(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	scope := agenttool.Scope{SessionID: "test", TurnID: "turn"}
	tool := FetchRefTool{Base: base}

	first, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text(), "branch=main") {
		t.Fatalf("first content = %q, want branch snapshot", first.Text())
	}
	if err := os.Rename(filepath.Join(root, "origin.git"), filepath.Join(root, "origin.git.offline")); err != nil {
		t.Fatal(err)
	}

	_, err = tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), Scope: scope})
	if err == nil {
		t.Fatal("second fetch should fail when origin is unavailable")
	}
}

func TestGitLogRefreshesExplicitBranchWithinFetchTTL(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	scope := agenttool.Scope{SessionID: "test", TurnID: "turn"}
	tool := LogTool{Base: base}
	first, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","limit":1}`), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text(), "init") {
		t.Fatalf("first content = %q, want initial commit", first.Text())
	}
	if !strings.Contains(first.Text(), "test@example.com") || !strings.Contains(first.Text(), "test\t") {
		t.Fatalf("first content = %q, want author identity", first.Text())
	}

	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\nfresh branch tip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fresh tip")
	runGit(t, work, "push", "origin", "main")

	second, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","limit":1}`), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Text(), "fresh tip") {
		t.Fatalf("second content = %q, want fresh remote branch tip despite fetch TTL", second.Text())
	}
	if !strings.Contains(second.Text(), "fetch_status=origin_refs_refreshed") {
		t.Fatalf("second content = %q, want explicit refresh status", second.Text())
	}
}

func TestRefToolsRequireExplicitRepo(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	fetchResult, err := (FetchRefTool{Base: base}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	ref := regexp.MustCompile(`(?m)^ref=([0-9a-f]{40})$`).FindStringSubmatch(fetchResult.Text())[1]

	_, err = (ReadFileRefTool{Base: base}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"ref":"`+ref+`","path":"README.md"}`), Scope: agenttool.Scope{}})
	if err == nil || !strings.Contains(err.Error(), "repo is required") {
		t.Fatalf("ReadFileRefTool error = %v, want repo required", err)
	}
	_, err = (SearchRefTool{Base: base}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"ref":"`+ref+`","query":"hello"}`), Scope: agenttool.Scope{}})
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
	searchResult, err := searchTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","query":"hello"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(searchResult.Text(), "README.md:1:hello") {
		t.Fatalf("search content = %q, want remote snapshot match", searchResult.Text())
	}
	noMatch, err := searchTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","query":"local only"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noMatch.Text(), "no matches") {
		t.Fatalf("search content = %q, want no local working tree match", noMatch.Text())
	}

	readTool := RepoReadFileTool{Base: base}
	readResult, err := readTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","path":"README.md"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readResult.Text(), "hello") || strings.Contains(readResult.Text(), "local only") {
		t.Fatalf("read content = %q, want remote snapshot only", readResult.Text())
	}
}

func TestRepoToolsDoNotUseStaleRefsWhenOriginIsUnavailable(t *testing.T) {
	root, work := testRepo(t)
	base := Base{
		Paths:   safety.WorkspacePolicy{Roots: []string{root}},
		Guard:   safety.NewCommandPolicy(),
		Timeout: 10 * time.Second,
	}
	scope := agenttool.Scope{SessionID: "test", TurnID: "turn"}
	searchTool := RepoSearchTool{Base: base}
	if _, err := searchTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","query":"hello"}`), Scope: scope}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "origin.git"), filepath.Join(root, "origin.git.offline")); err != nil {
		t.Fatal(err)
	}

	readTool := RepoReadFileTool{Base: base}
	_, err := readTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","path":"README.md"}`), Scope: scope})
	if err == nil {
		t.Fatal("read should fail when origin becomes unavailable")
	}

	gitcache.ResetForTest()
	_, err = readTool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"repo":"`+work+`","branch":"main","path":"README.md"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err == nil {
		t.Fatal("read should not fall back to cached refs when origin is unavailable")
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
