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
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestReadFileReturnsContentDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"app.go"}`)

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
	raw := json.RawMessage(`{"path":"wati-frontend-app/domains/connectors/src/Integration/_constants/IntegrationCards.tsx"}`)

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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"token"}`), registry.Runtime{})
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"catalog","path":"wati-frontend-app/domains"}`), registry.Runtime{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "connectors/catalog.ts") {
		t.Fatalf("expected search to resolve prefixed path, got %#v", result)
	}
}

func TestGitReadDefaultsToRemoteDefaultBranchNotCurrentCheckout(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"feature\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	runGit(t, work, "push", "origin", "feature")

	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"work/app.go"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ref=origin/main") || !strings.Contains(result.Content, `"main"`) || strings.Contains(result.Content, `"feature"`) {
		t.Fatalf("read content = %q, want remote default branch", result.Content)
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

func TestGitSearchDefaultsToRemoteDefaultBranch(t *testing.T) {
	root, work := testGitRepo(t)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"feature\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "app.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	runGit(t, work, "push", "origin", "feature")

	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"value"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ref=origin/main") || !strings.Contains(result.Content, `"main"`) || strings.Contains(result.Content, `"feature"`) {
		t.Fatalf("search content = %q, want remote default branch", result.Content)
	}
}

func TestGitSearchReturnsInvalidPatternError(t *testing.T) {
	root, _ := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"["}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
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
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"hello;"}`), registry.Runtime{Cache: registry.NewRuntimeCache()})
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
	runGit(t, work, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
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
