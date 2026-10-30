package code

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestReadFileReadsIndependentPathsTogether(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"paths":["a.go","b.go"],"source":"working_tree"}`)})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := result.Text()
	if !strings.Contains(text, "package a") || !strings.Contains(text, "package b") {
		t.Fatalf("expected both files, got %#v", result)
	}
}

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
	root := filepath.Join(parent, "example-frontend")
	filePath := filepath.Join(root, "domains", "connectors", "src", "Integration", "_constants", "IntegrationCards.tsx")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("export const cards = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	raw := json.RawMessage(`{"path":"example-frontend/domains/connectors/src/Integration/_constants/IntegrationCards.tsx","source":"working_tree"}`)

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
	root := filepath.Join(parent, "example-frontend")
	dir := filepath.Join(root, "domains", "connectors")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.ts"), []byte("const catalog = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"catalog","path":"example-frontend/domains","source":"working_tree"}`), Scope: agenttool.Scope{}})
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

func TestGitSearchTreatsRegexMetacharactersAsLiteralsByDefault(t *testing.T) {
	root, _ := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"[","source":"origin/main"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "no matches") {
		t.Fatalf("content = %q, want literal search result", result.Text())
	}
}

func TestGitSearchSupportsExplicitRegex(t *testing.T) {
	root, _ := testGitRepo(t)
	tool := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	_, err := tool.Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"query":"[","query_mode":"regex","source":"origin/main"}`), Scope: agenttool.Scope{SessionID: "test", TurnID: "turn"}})
	if err == nil || !strings.Contains(err.Error(), "code search failed") {
		t.Fatalf("error = %v, want invalid regex error", err)
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

func TestReadFileFindsRequestedLineBeyondSourcePrefixLimit(t *testing.T) {
	root, work := testGitRepo(t)
	const start = 40001
	content := strings.Repeat(strings.Repeat("x", 127)+"\n", start-1) + "const finalMarker = 42\n"
	if err := os.WriteFile(filepath.Join(work, "large.go"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "large.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "large source fixture")
	runGit(t, work, "push", "origin", "main")
	reader := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "read"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	for _, source := range []string{"origin/main", "working_tree"} {
		raw, _ := json.Marshal(map[string]any{"path": "work/large.go", "source": source, "start_line": start, "max_lines": 1})
		result, err := reader.Execute(t.Context(), agenttool.Call{Arguments: raw, Scope: scope})
		if err != nil || !strings.Contains(result.Text(), "40001  const finalMarker = 42") {
			t.Errorf("source=%s: selected line missing: %q %v", source, result.Text(), err)
		}
	}
}

func TestCappedBufferBoundsReaderFromCopies(t *testing.T) {
	var output cappedBuffer
	output.limit = 4
	input := struct{ io.Reader }{strings.NewReader("0123456789")}
	count, err := io.Copy(&output, input)
	if err != nil || count != 10 || output.String() != "0123" {
		t.Fatalf("copy bypassed byte limit: count=%d output=%q err=%v", count, output.String(), err)
	}
}

func TestReadLineRangeHandlesLongLinesWithoutReadingOutsideWindow(t *testing.T) {
	for _, fixture := range []struct {
		input string
		start int
		want  string
	}{
		{"first\n" + strings.Repeat("x", 2<<20), 1, "     1  first\n"},
		{strings.Repeat("x", 2<<20) + "\nlast\n", 2, "     2  last\n"},
		{"first\r\nlast\r\n", 2, "     2  last\n"},
	} {
		got, err := readLineRange(t.Context(), strings.NewReader(fixture.input), fixture.start, 1)
		if err != nil || got != fixture.want {
			t.Fatalf("line window: got=%q err=%v", got, err)
		}
	}
	got, err := readLineRange(t.Context(), strings.NewReader(strings.Repeat("x", 2<<20)), 1, 1)
	if err != nil || !strings.HasSuffix(got, "...[truncated]\n") || len(got) > 200_100 {
		t.Fatalf("long selected line: bytes=%d err=%v", len(got), err)
	}
}

func TestSearchReportsLimitedResultsAsTruncated(t *testing.T) {
	root, work := testGitRepo(t)
	if err := os.WriteFile(filepath.Join(work, "matches.go"), []byte("needle\nneedle\nneedle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "matches.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "search fixture")
	runGit(t, work, "push", "origin", "main")
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "search"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	search := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	for _, source := range []string{"origin/main", "working_tree"} {
		raw, _ := json.Marshal(map[string]any{"path": "work", "query": "needle", "source": source, "limit": 1})
		result, err := search.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: raw})
		if err != nil || !result.Truncated || !strings.Contains(result.Text(), "...[truncated") {
			t.Fatalf("source=%s completeness lost: %+v %v", source, result, err)
		}
	}
}

func BenchmarkReadLineWindow(b *testing.B) {
	for _, lines := range []int{100, 40000} {
		content := strings.Repeat(strings.Repeat("x", 127)+"\n", lines)
		b.Run(fmt.Sprint(lines), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := readLineRange(b.Context(), strings.NewReader(content), 1, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
