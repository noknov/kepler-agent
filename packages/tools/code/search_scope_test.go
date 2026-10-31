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

func TestSearchPathAndGlobConstrainTheSameSourceScope(t *testing.T) {
	for _, fixture := range []struct{ path, glob, wanted string }{
		{"sub", "*.go", "sub/inside.go"},
		{"pkg[1]", "*.go", "pkg[1]/inside.go"},
		{"..generated", "*.go", "..generated/inside.go"},
		{"policy/.docker", "", "policy/.docker/notes.go"},
		{"sub/note.txt", "*.go", "sub/note.txt"},
	} {
		t.Run(fixture.path, func(t *testing.T) {
			root, work := testGitRepo(t)
			files := map[string]string{
				"outside.go":                 "needle distractor\n",
				"sub/inside.go":              "needle wanted\n",
				"sub/note.txt":               "needle text\n",
				"pkg[1]/inside.go":           "needle wanted\n",
				"pkg1/decoy.go":              "needle distractor\n",
				"..generated/inside.go":      "needle wanted\n",
				"policy/.docker/config.json": "needle private fixture\n",
				"policy/.docker/notes.go":    "needle wanted\n",
			}
			for path, body := range files {
				target := filepath.Join(work, path)
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runGit(t, work, "add", "-f", ".")
			runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "source scope fixture")
			runGit(t, work, "push", "origin", "main")
			scope := agenttool.Scope{SessionID: t.Name(), TurnID: "search"}
			t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
			search := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
			raw, _ := json.Marshal(map[string]any{"path": "work/" + fixture.path, "query": "needle", "glob": fixture.glob, "source": "origin/main"})
			result, err := search.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: raw})
			if err != nil {
				t.Fatal(err)
			}
			hits := 0
			for _, line := range strings.Split(result.Text(), "\n") {
				if strings.HasPrefix(line, "work/") {
					hits++
					if !strings.HasPrefix(line, "work/"+fixture.wanted+":1:") {
						t.Errorf("search escaped requested path/glob: %s", line)
					}
				}
			}
			if hits != 1 {
				t.Fatalf("expected one scoped hit, got %d: %s", hits, result.Text())
			}
		})
	}
}

func TestReadAcceptsLocalPathBeginningWithTwoDots(t *testing.T) {
	root, work := testGitRepo(t)
	if err := os.WriteFile(filepath.Join(work, "..generated.go"), []byte("package generated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "..generated.go")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "local path fixture")
	runGit(t, work, "push", "origin", "main")
	scope := agenttool.Scope{SessionID: t.Name(), TurnID: "read"}
	t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
	result, err := (ReadFileTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}).Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: json.RawMessage(`{"path":"work/..generated.go","source":"origin/main"}`)})
	if err != nil || !strings.Contains(result.Text(), "package generated") {
		t.Fatalf("valid local path rejected as parent traversal: %q %v", result.Text(), err)
	}
}

func TestSearchPathKindComesFromSourceSnapshot(t *testing.T) {
	root, work := testGitRepo(t)
	if err := os.WriteFile(filepath.Join(work, "shape"), []byte("needle file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "shape")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "file snapshot")
	runGit(t, work, "branch", "as-file")
	runGit(t, work, "push", "origin", "as-file")
	runGit(t, work, "rm", "shape")
	if err := os.Mkdir(filepath.Join(work, "shape"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"inside.go": "needle inside\n", "note.txt": "needle note\n"} {
		if err := os.WriteFile(filepath.Join(work, "shape", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, work, "add", "shape")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "directory snapshot")
	runGit(t, work, "push", "origin", "main")
	search := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	for _, fixture := range []struct{ checkout, source, wanted string }{{"main", "origin/as-file", "work/shape:1:needle file"}, {"as-file", "origin/main", "work/shape/inside.go:1:needle inside"}} {
		runGit(t, work, "checkout", fixture.checkout)
		scope := agenttool.Scope{SessionID: t.Name(), TurnID: fixture.checkout}
		t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
		raw, _ := json.Marshal(map[string]any{"path": "work/shape", "source": fixture.source, "glob": "*.go", "query": "needle"})
		result, err := search.Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: raw})
		if err != nil || !strings.Contains(result.Text(), fixture.wanted) || strings.Contains(result.Text(), "needle note") {
			t.Errorf("checkout determined source path kind: %s %v", result.Text(), err)
		}
	}
}

func TestContainerSearchUsesRepositoryMetadataAndReadablePathPolicy(t *testing.T) {
	for _, name := range []string{".source", "secrets"} {
		t.Run(name, func(t *testing.T) {
			root, work := testGitRepo(t)
			if err := os.Rename(work, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			scope := agenttool.Scope{SessionID: t.Name(), TurnID: "search"}
			t.Cleanup(func() { agenttool.ClearTurnCache(scope.SessionID, scope.TurnID) })
			raw, _ := json.Marshal(map[string]any{"query": "value", "source": "origin/main"})
			result, err := (SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}).Execute(t.Context(), agenttool.Call{Scope: scope, Arguments: raw})
			if name == "secrets" {
				if strings.Contains(result.Text(), "const value") {
					t.Fatalf("container bypassed sensitive repository policy: %s", result.Text())
				}
			} else if err != nil || !strings.Contains(result.Text(), ".source/app.go:2:") {
				t.Fatalf("literal repository name hid valid source: %s %v", result.Text(), err)
			}
		})
	}
}
