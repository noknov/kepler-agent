package code

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

func TestReadFileReturnsContentDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"app.go","source":"working_tree"}`)

	result, err := tool.Execute(context.Background(), raw, registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.NeedsUserInput || result.Content == "" {
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

	result, err := tool.Execute(context.Background(), raw, registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "export const cards") {
		t.Fatalf("expected prefixed path to resolve, got %#v", result)
	}
}

func TestReadFileRejectsSensitiveFilesWithoutPrompting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":".env"}`), registry.Runtime{})
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"token","source":"working_tree"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.NeedsUserInput || result.Content == "" || result.Content == "no matches" {
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"catalog","path":"wati-frontend-app/domains","source":"working_tree"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "connectors/catalog.ts") {
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"work/app.go","source":"origin/main"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ref=origin/main") || !strings.Contains(result.Content, `"main"`) || strings.Contains(result.Content, `"feature"`) {
		t.Fatalf("read content = %q, want explicit remote ref", result.Content)
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
	rt := registry.Runtime{Cache: registry.NewRuntimeCache()}
	mainResult, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"work/app.go","source":"origin/main"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	featureResult, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"work/app.go","source":"origin/feature"}`), rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mainResult.Content, `"main"`) || strings.Contains(mainResult.Content, `"feature"`) {
		t.Fatalf("main content = %q", mainResult.Content)
	}
	if !strings.Contains(featureResult.Content, `"feature"`) || strings.Contains(featureResult.Content, `"main"`) {
		t.Fatalf("feature content = %q", featureResult.Content)
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"value","source":"origin/main"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ref=origin/main") || !strings.Contains(result.Content, `"main"`) || strings.Contains(result.Content, `"feature"`) {
		t.Fatalf("search content = %q, want explicit remote ref", result.Content)
	}
}

func TestGitSearchDefaultsToFreshCurrentBranchRemoteRef(t *testing.T) {
	root, work := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	first, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"value"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Content, `"main"`) || !strings.Contains(first.Content, "fetch_status=origin_refs_refreshed") {
		t.Fatalf("first content = %q, want refreshed origin/main", first.Content)
	}

	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"fresh\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "fresh remote tip")
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "reset", "--hard", "HEAD~1")

	second, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"value"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Content, `"fresh"`) || strings.Contains(second.Content, `"main"`) {
		t.Fatalf("second content = %q, want freshly fetched remote branch tip", second.Content)
	}
	if !strings.Contains(second.Content, "fetch_status=origin_refs_refreshed") {
		t.Fatalf("second content = %q, want refreshed fetch status", second.Content)
	}
}

func TestGitSearchUsesCheckedOutBranchUpstreamRef(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "branch", "-m", "main", "mt-main")
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"value"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ref=origin/main") || strings.Contains(result.Content, "origin/mt-main") {
		t.Fatalf("search content = %q, want the branch upstream ref", result.Content)
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

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"work/app.go"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `ref=origin/main`) || !strings.Contains(result.Content, `"fresh"`) || strings.Contains(result.Content, `"main"`) {
		t.Fatalf("read content = %q, want fresh remote branch snapshot instead of stale working tree", result.Content)
	}
	if !strings.Contains(result.Content, "fetch_status=origin_refs_refreshed") {
		t.Fatalf("read content = %q, want refreshed fetch status", result.Content)
	}
}

func TestGitSearchReturnsInvalidPatternError(t *testing.T) {
	root, _ := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"[","source":"origin/main"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"hello;","source":"origin/main"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "no matches") {
		t.Fatalf("content = %q, want safe semicolon query to execute", result.Content)
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
