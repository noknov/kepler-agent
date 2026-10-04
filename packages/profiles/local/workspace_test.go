package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRejectsEscapeSensitiveAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	for _, path := range []string{"../outside", ".env", "escape/file.txt"} {
		if _, err := workspace.Resolve(path, true); err == nil {
			t.Fatalf("expected %q to be rejected", path)
		}
	}
	paths, err := workspace.SensitivePaths()
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != ".env" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}

func TestWorkspaceResolvesSymlinkedTemporaryRoot(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	workspace, err := NewWorkspace(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	resolved, err := filepath.EvalSymlinks(workspace.Temp)
	if err != nil || resolved != workspace.Temp {
		t.Fatalf("temp=%q resolved=%q error=%v", workspace.Temp, resolved, err)
	}
}
