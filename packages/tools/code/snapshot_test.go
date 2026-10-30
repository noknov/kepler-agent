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

func TestCodeEvidencePinsContentAndCommitDuringRefRefresh(t *testing.T) {
	for _, operation := range []string{"read", "search"} {
		t.Run(operation, func(t *testing.T) {
			root, work := testGitRepo(t)
			original := strings.TrimSpace(runGitOutput(t, work, "rev-parse", "HEAD"))
			if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nconst value = \"advanced\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			runGit(t, work, "add", "app.go")
			runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "next local snapshot")
			next := strings.TrimSpace(runGitOutput(t, work, "rev-parse", "HEAD"))
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			shim := t.TempDir()
			script := `#!/bin/sh
if [ "$3" = "show" ] || [ "$3" = "cat-file" ]; then
 "$KEPLER_TEST_REAL_GIT" "$@"
 status=$?
 "$KEPLER_TEST_REAL_GIT" -C "$2" update-ref refs/remotes/origin/main "$KEPLER_TEST_NEXT_COMMIT"
 exit "$status"
fi
if [ "$3" = "grep" ]; then
 "$KEPLER_TEST_REAL_GIT" -C "$2" update-ref refs/remotes/origin/main "$KEPLER_TEST_NEXT_COMMIT"
fi
exec "$KEPLER_TEST_REAL_GIT" "$@"
`
			if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KEPLER_TEST_REAL_GIT", realGit)
			t.Setenv("KEPLER_TEST_NEXT_COMMIT", next)
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			scope := agenttool.Scope{SessionID: t.Name(), TurnID: operation}
			t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
			var result agenttool.Result
			policy := safety.WorkspacePolicy{Roots: []string{root}}
			if operation == "read" {
				result, err = (ReadFileTool{Paths: policy}).Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: json.RawMessage(`{"path":"work/app.go","source":"origin/main"}`)})
			} else {
				result, err = (SearchTool{Paths: policy}).Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: json.RawMessage(`{"path":"work","source":"origin/main","query":"value"}`)})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Text(), `"main"`) || strings.Contains(result.Text(), `"advanced"`) || !strings.Contains(result.Text(), "commit="+original) {
				t.Fatalf("content and evidence commit describe different snapshots: %s", result.Text())
			}
			if operation == "search" && !strings.Contains(result.Text(), "work/app.go:2:") {
				t.Fatalf("snapshot prefix leaked into file identity: %s", result.Text())
			}
			if got := strings.TrimSpace(runGitOutput(t, work, "rev-parse", "origin/main")); got != next {
				t.Fatalf("fixture did not advance mutable ref: %s", got)
			}
		})
	}
}
