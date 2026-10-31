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

func TestWorkingTreeSearchSharesReadablePathPolicy(t *testing.T) {
	root := t.TempDir()
	paths := []string{"public.txt", "secrets/token.txt", "sub/CREDENTIALS.json", "sub/client.PEM"}
	for _, path := range paths {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("policy-sentinel\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("policy-sentinel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "ripgreprc")
	if err := os.WriteFile(config, []byte("--follow\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", config)
	search := SearchTool{Paths: safety.WorkspacePolicy{Roots: []string{root}}}
	for _, glob := range []string{"", "**/*"} {
		raw, _ := json.Marshal(map[string]any{"query": "policy-sentinel", "source": "working_tree", "glob": glob})
		result, err := search.Execute(t.Context(), agenttool.Call{Arguments: raw})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Text(), "public.txt:1:policy-sentinel") {
			t.Fatalf("public result missing: %s", result.Text())
		}
		if strings.Contains(result.Text(), "linked.txt") {
			t.Errorf("ambient ripgrep configuration followed an outside-root symlink: %s", result.Text())
		}
		for _, path := range paths[1:] {
			if !safety.IsSensitivePath(path) {
				t.Fatalf("fixture is outside the readable-path policy: %s", path)
			}
			if strings.Contains(result.Text(), filepath.Base(path)) {
				t.Errorf("glob=%q exposed a rejected path %s: %s", glob, path, result.Text())
			}
		}
	}
}
