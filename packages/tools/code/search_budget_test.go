package code

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/safety"
)

func TestSearchStopsBeforeFetchingAnUnusedRepository(t *testing.T) {
	root, work := testGitRepo(t)
	first := filepath.Join(root, "a")
	if err := os.Rename(work, first); err != nil {
		t.Fatal(err)
	}
	unused := filepath.Join(root, "b")
	runGit(t, root, "clone", filepath.Join(root, "origin.git"), unused)
	runGit(t, unused, "remote", "set-url", "origin", filepath.Join(root, "missing-origin.git"))
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	canonicalUnused, err := filepath.EvalSymlinks(unused)
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	marker := filepath.Join(shim, "unused-fetch")
	script := `#!/bin/sh
if [ "$1" = "-C" ] && [ "$2" = "$KEPLER_TEST_UNUSED_REPO" ] && [ "$3" = "fetch" ]; then
 : > "$KEPLER_TEST_UNUSED_FETCH_LOG"
fi
exec "$KEPLER_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEPLER_TEST_REAL_GIT", realGit)
	t.Setenv("KEPLER_TEST_UNUSED_REPO", canonicalUnused)
	t.Setenv("KEPLER_TEST_UNUSED_FETCH_LOG", marker)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "search"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	result, err := (SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}).Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: json.RawMessage(`{"query":"value","source":"origin/main","limit":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("unused repository was fetched: %v", err)
	}
	if !result.Truncated || !strings.Contains(result.Text(), "a/app.go:2:") || strings.Contains(result.Text(), "Search warnings:") {
		t.Fatalf("result budget did not end source work: %+v", result)
	}
}
