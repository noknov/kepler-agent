package code

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestGitReadFindsNewRemoteFileWithoutCheckout(t *testing.T) {
	root, work := testGitRepo(t)
	if err := os.Mkdir(filepath.Join(work, "newdir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "newdir", "new.go"), []byte("package newsource\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "new remote file")
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "reset", "--hard", "HEAD~1")
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "read"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(work, alias); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ root, path string }{
		{root, "work/newdir/new.go"},
		{root, filepath.Join(work, "newdir", "new.go")},
		{work, "work/newdir/new.go"},
		{alias, "workspace/newdir/new.go"},
	} {
		reader := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{fixture.root}}}
		raw, _ := json.Marshal(map[string]any{"path": fixture.path, "source": "origin/main"})
		result, err := reader.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: raw})
		if err != nil || !strings.Contains(result.Text(), "package newsource") {
			t.Fatalf("fresh committed path %q required a checkout: %q %v", fixture.path, result.Text(), err)
		}
	}

	if _, err := os.Stat(filepath.Join(work, "newdir", "new.go")); !os.IsNotExist(err) {
		t.Fatalf("source read changed checkout: %v", err)
	}
}

func TestGitReadDoesNotFollowWorkingTreeSymlink(t *testing.T) {
	root, work := testGitRepo(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private external fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "app.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "app.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "read"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	reader := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	result, err := reader.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: json.RawMessage(`{"path":"work/app.go","source":"origin/main"}`)})
	if err != nil || !strings.Contains(result.Text(), "package app") || strings.Contains(result.Text(), "private external") {
		t.Fatalf("committed read used working-tree symlink: %q %v", result.Text(), err)
	}
	if _, err := reader.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: json.RawMessage(`{"path":"work/app.go","source":"working_tree"}`)}); err == nil {
		t.Fatal("working-tree read followed an external symlink")
	}
}

func TestGitReadRetainsFileAndWorkspaceBoundaries(t *testing.T) {
	root, work := testGitRepo(t)
	if err := os.Mkdir(filepath.Join(work, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "sub", "file.go"), []byte("public source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "directory fixture")
	runGit(t, work, "push", "origin", "main")
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("private external fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	traversal, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "read"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	reader := ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	for _, path := range []string{"work/sub", "work/.env", outside, traversal} {
		raw, _ := json.Marshal(map[string]any{"path": path, "source": "origin/main"})
		if result, err := reader.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: raw}); err == nil {
			t.Errorf("non-file or unauthorized path %q returned content: %q", path, result.Text())
		}
	}
}
