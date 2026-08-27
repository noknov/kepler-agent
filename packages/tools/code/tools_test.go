package code

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/safety"
)

func TestReadFileReturnsContentDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"app.go","source":"working_tree"}`)

	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: raw, Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.NeedsUserInput || result.Text() == "" {
		t.Fatalf("expected file content, got %#v", result)
	}
}

func TestReadFileAcceptsWorkspaceRootBasenamePrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "wati-frontend-app")
	filePath := filepath.Join(root, "domains", "connectors", "src", "Integration", "_constants", "IntegrationCards.tsx")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("export const cards = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"wati-frontend-app/domains/connectors/src/Integration/_constants/IntegrationCards.tsx","source":"working_tree"}`)

	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: raw, Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Text(), "export const cards") {
		t.Fatalf("expected prefixed path to resolve, got %#v", result)
	}
}

func TestReadFileRejectsSensitiveFilesWithoutPrompting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":".env"}`), Scope: agenttool.Scope{}})
	if err == nil {
		t.Fatal("expected sensitive file read to be rejected")
	}
}

func TestSearchReturnsResultsDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nvar token = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"token","source":"working_tree"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.NeedsUserInput || result.Text() == "" || result.Text() == "no matches" {
		t.Fatalf("expected search results, got %#v", result)
	}
}

func TestSearchAcceptsWorkspaceRootBasenamePrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "wati-frontend-app")
	dir := filepath.Join(root, "domains", "connectors")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.ts"), []byte("const catalog = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"catalog","path":"wati-frontend-app/domains","source":"working_tree"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Text(), "connectors/catalog.ts") {
		t.Fatalf("expected search to resolve prefixed path, got %#v", result)
	}
}

func TestGitReadUsesExplicitRemoteRefNotCurrentCheckout(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"feature\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	runGit(t, work, "push", "origin", "feature")

	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"work/app.go","source":"origin/main"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "ref=origin/main") || !strings.Contains(result.Text(), `"main"`) || strings.Contains(result.Text(), `"feature"`) {
		t.Fatalf("read content = %q, want explicit remote ref", result.Text())
	}
}

func TestGitReadAllowsConcurrentBranchSnapshotsWithoutCheckout(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"feature\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	runGit(t, work, "push", "origin", "feature")

	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	scope := agenttool.Scope{SessionID: "test", TurnID: "turn"}
	mainResult, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"work/app.go","source":"origin/main"}`), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	featureResult, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"work/app.go","source":"origin/feature"}`), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mainResult.Text(), `"main"`) || strings.Contains(mainResult.Text(), `"feature"`) {
		t.Fatalf("main content = %q", mainResult.Text())
	}
	if !strings.Contains(featureResult.Text(), `"feature"`) || strings.Contains(featureResult.Text(), `"main"`) {
		t.Fatalf("feature content = %q", featureResult.Text())
	}
	branch := strings.TrimSpace(runGitOutput(t, work, "branch", "--show-current"))
	if branch != "feature" {
		t.Fatalf("branch changed to %q, want feature", branch)
	}
}

func TestGitSearchUsesExplicitRemoteRef(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"feature\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	runGit(t, work, "push", "origin", "feature")

	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"value","source":"origin/main"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "ref=origin/main") || !strings.Contains(result.Text(), `"main"`) || strings.Contains(result.Text(), `"feature"`) {
		t.Fatalf("search content = %q, want explicit remote ref", result.Text())
	}
}

func TestGitSearchDefaultsToFreshCurrentBranchRemoteRef(t *testing.T) {
	root, work := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	first, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"value"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text(), `"main"`) || !strings.Contains(first.Text(), "fetch_status=origin_refs_refreshed") {
		t.Fatalf("first content = %q, want refreshed origin/main", first.Text())
	}

	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"fresh\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fresh remote tip")
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "reset", "--hard", "HEAD~1")

	second, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"value"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Text(), `"fresh"`) || strings.Contains(second.Text(), `"main"`) {
		t.Fatalf("second content = %q, want freshly fetched remote branch tip", second.Text())
	}
	if !strings.Contains(second.Text(), "fetch_status=origin_refs_refreshed") {
		t.Fatalf("second content = %q, want refreshed fetch status", second.Text())
	}
}

func TestGitSearchUsesCheckedOutBranchUpstreamRef(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "branch", "-m", "main", "mt-main")
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"value"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "ref=origin/main") || strings.Contains(result.Text(), "origin/mt-main") {
		t.Fatalf("search content = %q, want the branch upstream ref", result.Text())
	}
}

func TestGitReadDefaultsToFreshCurrentBranchRemoteRef(t *testing.T) {
	root, work := testGitRepo(t)
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"fresh\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fresh remote tip")
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "reset", "--hard", "HEAD~1")

	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"path":"work/app.go"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), `ref=origin/main`) || !strings.Contains(result.Text(), `"fresh"`) || strings.Contains(result.Text(), `"main"`) {
		t.Fatalf("read content = %q, want fresh remote branch snapshot instead of stale working tree", result.Text())
	}
	if !strings.Contains(result.Text(), "fetch_status=origin_refs_refreshed") {
		t.Fatalf("read content = %q, want refreshed fetch status", result.Text())
	}
}

func TestGitSearchReturnsInvalidPatternError(t *testing.T) {
	root, _ := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"[","source":"origin/main"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err == nil {
		t.Fatal("Execute() succeeded, want invalid pattern error")
	}
	if !strings.Contains(err.Error(), "code search failed") {
		t.Fatalf("error = %v, want code search failed", err)
	}
}

func TestGitSearchAllowsSemicolonLiterals(t *testing.T) {
	root, _ := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"hello;","source":"origin/main"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "no matches") {
		t.Fatalf("content = %q, want safe semicolon query to execute", result.Text())
	}
}

func testGitRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"main\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	runGit(t, work, "remote", "add", "origin", origin)
	runGit(t, work, "push", "-u", "origin", "main")
	return root, work
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, dir, args...)
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
