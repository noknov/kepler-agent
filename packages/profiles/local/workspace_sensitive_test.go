package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSensitivePathsIncludesRepositoryCredentialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".git", "config")
	if err := os.WriteFile(config, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := (Workspace{Root: root}).SensitivePaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path == config {
			return
		}
	}
	t.Fatalf("sensitive paths=%#v, want %s", paths, config)
}
