package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverRepositoriesFindsGitRepos(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "private-service")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module private-service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repos := DiscoverRepositories([]string{root})
	if len(repos) != 1 || repos[0].Name != "private-service/" || repos[0].Stack != "Go" {
		t.Fatalf("repos=%+v", repos)
	}
}
